package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/emicklei/go-restful/v3"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/managedfields"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apiserver/pkg/endpoints"
	"k8s.io/apiserver/pkg/endpoints/discovery"
	genericapifilters "k8s.io/apiserver/pkg/endpoints/filters"
	openapinamer "k8s.io/apiserver/pkg/endpoints/openapi"
	genericrequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/generic"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/server/routes"
	"k8s.io/apiserver/pkg/storage/storagebackend"
	"k8s.io/apiserver/pkg/util/openapi"

	"k8s.io/kubernetes/pkg/api/legacyscheme"
	apiapps "k8s.io/kubernetes/pkg/apis/apps"
	apiappsv1 "k8s.io/kubernetes/pkg/apis/apps/v1"
	generatedopenapi "k8s.io/kubernetes/pkg/generated/openapi"
	deploystorage "k8s.io/kubernetes/pkg/registry/apps/deployment/storage"

	openapibuilder3 "k8s.io/kube-openapi/pkg/builder3"
)

func main() {
	scheme := legacyscheme.Scheme
	codecs := serializer.NewCodecFactory(scheme)
	parameterCodec := runtime.NewParameterCodec(scheme)

	utilruntime.Must(apiapps.AddToScheme(scheme))
	utilruntime.Must(apiappsv1.AddToScheme(scheme))
	metav1.AddToGroupVersion(scheme, schema.GroupVersion{Version: "v1"})

	cfg := storagebackend.NewDefaultConfig(
		"/apis",
		codecs.LegacyCodec(apiappsv1.SchemeGroupVersion))
	cfg.Transport = storagebackend.TransportConfig{
		ServerList: []string{"127.0.0.1:2379"},
	}

	handler := server.NewAPIServerHandler(
		"demo",
		codecs,
		func(handler http.Handler) http.Handler {
			handler = genericapifilters.WithRequestInfo(
				handler, &genericrequest.RequestInfoFactory{})
			return handler
		},
		http.NotFoundHandler())
	discoveryManager := discovery.NewRootAPIsHandler(
		discovery.DefaultAddresses{DefaultAddress: "127.0.0.1"},
		codecs,
	)

	openapiConfig := server.DefaultOpenAPIV3Config(
		openapi.GetOpenAPIDefinitionsWithoutDisabledFeatures(
			generatedopenapi.GetOpenAPIDefinitions),
		openapinamer.NewDefinitionNamer(scheme),
	)
	openapiSpec, err := openapibuilder3.BuildOpenAPIDefinitionsForResources(
		openapiConfig, "k8s.io/api/apps/v1.Deployment")
	if err != nil {
		panic(err)
	}
	typeConverter, err := managedfields.NewTypeConverter(openapiSpec, false)
	if err != nil {
		panic(err)
	}

	apiGroupInfo := server.NewDefaultAPIGroupInfo(
		apiappsv1.GroupName, scheme, parameterCodec, codecs)
	{
		opts := generic.RESTOptions{
			StorageConfig:  cfg.ForResource(schema.GroupResource{Group: apiappsv1.GroupName, Resource: "deployments"}),
			Decorator:      generic.UndecoratedStorage,
			ResourcePrefix: "/apis",
		}
		storage, err := deploystorage.NewStorage(opts)
		utilruntime.Must(err)
		apiGroupInfo.VersionedResourcesStorageMap[apiappsv1.SchemeGroupVersion.Version] = map[string]rest.Storage{
			"deployments": storage.Deployment,
		}
	}

	if err := installAPIGroupInfo(
		handler.GoRestfulContainer, typeConverter,
		codecs, discoveryManager,
		&apiGroupInfo,
	); err != nil {
		panic(err)
	}

	routes.OpenAPI{Config: openapiConfig}.InstallV3(
		handler.GoRestfulContainer,
		handler.NonGoRestfulMux)

	handler.GoRestfulContainer.Add(discoveryManager.WebService())
	if err := http.ListenAndServe(":8080", handler); err != nil {
		panic(err)
	}
}

func installAPIGroupInfo(
	restfulContainer *restful.Container,
	typeConverter managedfields.TypeConverter,
	serializer runtime.NegotiatedSerializer,
	discoveryGroupManager discovery.GroupManager,
	apiGroupInfos ...*server.APIGroupInfo,
) error {
	for _, apiGroupInfo := range apiGroupInfos {
		if len(apiGroupInfo.PrioritizedVersions) == 0 {
			return fmt.Errorf("no version priority set for %#v", *apiGroupInfo)
		}
		// Do not register empty group or empty version.  Doing so claims /apis/ for the wrong entity to be returned.
		// Catching these here places the error  much closer to its origin
		if len(apiGroupInfo.PrioritizedVersions[0].Group) == 0 {
			return fmt.Errorf("cannot register handler with an empty group for %#v", *apiGroupInfo)
		}
		if len(apiGroupInfo.PrioritizedVersions[0].Version) == 0 {
			return fmt.Errorf("cannot register handler with an empty version for %#v", *apiGroupInfo)
		}
	}

	for _, apiGroupInfo := range apiGroupInfos {
		if err := installAPIResources("/apis", apiGroupInfo, restfulContainer, typeConverter); err != nil {
			return fmt.Errorf("unable to install api resources: %v", err)
		}

		apiVersionsForDiscovery := []metav1.GroupVersionForDiscovery{}
		for _, groupVersion := range apiGroupInfo.PrioritizedVersions {
			if len(apiGroupInfo.VersionedResourcesStorageMap[groupVersion.Version]) == 0 {
				continue
			}
			apiVersionsForDiscovery = append(apiVersionsForDiscovery, metav1.GroupVersionForDiscovery{
				GroupVersion: groupVersion.String(),
				Version:      groupVersion.Version,
			})
		}
		preferredVersionForDiscovery := metav1.GroupVersionForDiscovery{
			GroupVersion: apiGroupInfo.PrioritizedVersions[0].String(),
			Version:      apiGroupInfo.PrioritizedVersions[0].Version,
		}
		apiGroup := metav1.APIGroup{
			Name:             apiGroupInfo.PrioritizedVersions[0].Group,
			Versions:         apiVersionsForDiscovery,
			PreferredVersion: preferredVersionForDiscovery,
		}

		discoveryGroupManager.AddGroup(apiGroup)
		restfulContainer.Add(discovery.NewAPIGroupHandler(
			serializer, apiGroup).WebService())
	}
	return nil
}

func installAPIResources(
	apiPrefix string,
	apiGroupInfo *server.APIGroupInfo,
	restfulContainer *restful.Container,
	typeConverter managedfields.TypeConverter,
) error {
	for _, groupVersion := range apiGroupInfo.PrioritizedVersions {
		if len(apiGroupInfo.VersionedResourcesStorageMap[groupVersion.Version]) == 0 {
			continue
		}
		apiGroupVersion, err := getAPIGroupVersion(apiGroupInfo, groupVersion, apiPrefix)
		if err != nil {
			return err
		}
		if apiGroupInfo.OptionsExternalVersion != nil {
			apiGroupVersion.OptionsExternalVersion = apiGroupInfo.OptionsExternalVersion
		}
		apiGroupVersion.TypeConverter = typeConverter
		// apiGroupVersion.MaxRequestBodyBytes = s.maxRequestBodyBytes

		_, _, err = apiGroupVersion.InstallREST(restfulContainer)
		if err != nil {
			return fmt.Errorf("unable to setup API %v: %v", apiGroupInfo, err)
		}
	}
	return nil
}

func getAPIGroupVersion(apiGroupInfo *server.APIGroupInfo, groupVersion schema.GroupVersion, apiPrefix string) (*endpoints.APIGroupVersion, error) {
	storage := make(map[string]rest.Storage)
	for k, v := range apiGroupInfo.VersionedResourcesStorageMap[groupVersion.Version] {
		if strings.ToLower(k) != k {
			return nil, fmt.Errorf("resource names must be lowercase only, not %q", k)
		}
		storage[k] = v
	}
	version := newAPIGroupVersion(apiGroupInfo, groupVersion)
	version.Root = apiPrefix
	version.Storage = storage
	return version, nil
}

func newAPIGroupVersion(apiGroupInfo *server.APIGroupInfo, groupVersion schema.GroupVersion) *endpoints.APIGroupVersion {
	allServedVersionsByResource := map[string][]string{}
	for version, resourcesInVersion := range apiGroupInfo.VersionedResourcesStorageMap {
		for resource := range resourcesInVersion {
			if len(groupVersion.Group) == 0 {
				allServedVersionsByResource[resource] = append(allServedVersionsByResource[resource], version)
			} else {
				allServedVersionsByResource[resource] = append(allServedVersionsByResource[resource], fmt.Sprintf("%s/%s", groupVersion.Group, version))
			}
		}
	}

	return &endpoints.APIGroupVersion{
		GroupVersion:                groupVersion,
		AllServedVersionsByResource: allServedVersionsByResource,
		MetaGroupVersion:            apiGroupInfo.MetaGroupVersion,

		ParameterCodec:        apiGroupInfo.ParameterCodec,
		Serializer:            apiGroupInfo.NegotiatedSerializer,
		Creater:               apiGroupInfo.Scheme,
		Convertor:             apiGroupInfo.Scheme,
		ConvertabilityChecker: apiGroupInfo.Scheme,
		UnsafeConvertor:       runtime.UnsafeObjectConvertor(apiGroupInfo.Scheme),
		Defaulter:             apiGroupInfo.Scheme,
		Typer:                 apiGroupInfo.Scheme,
		Namer:                 runtime.Namer(meta.NewAccessor()),

		EquivalentResourceRegistry: runtime.NewEquivalentResourceRegistry(),

		// Admit:             s.admissionControl,
		// MinRequestTimeout: s.minRequestTimeout,
		// Authorizer:        s.Authorizer,
	}
}
