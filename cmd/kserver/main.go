package main

import (
	"net/http"

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

	cfg := storagebackend.NewDefaultConfig("/apis", codecs.LegacyCodec(apiappsv1.SchemeGroupVersion))
	cfg.Transport = storagebackend.TransportConfig{
		ServerList: []string{"127.0.0.1:2379"},
	}
	opts := generic.RESTOptions{
		StorageConfig:  cfg.ForResource(schema.GroupResource{Group: apiappsv1.GroupName, Resource: "deployments"}),
		Decorator:      generic.UndecoratedStorage,
		ResourcePrefix: "/apis",
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

	gversion := endpoints.APIGroupVersion{
		Storage: map[string]rest.Storage{},

		Root:                   "/apis",
		GroupVersion:           apiappsv1.SchemeGroupVersion,
		OptionsExternalVersion: &schema.GroupVersion{Version: "v1"},

		AllServedVersionsByResource: map[string][]string{
			"deployments": []string{"apps/v1"},
		},
		MetaGroupVersion: nil,

		ParameterCodec:             parameterCodec,
		Serializer:                 codecs,
		Creater:                    scheme,
		Convertor:                  scheme,
		ConvertabilityChecker:      scheme,
		UnsafeConvertor:            runtime.UnsafeObjectConvertor(scheme),
		Defaulter:                  scheme,
		Typer:                      scheme,
		TypeConverter:              typeConverter,
		Namer:                      runtime.Namer(meta.NewAccessor()),
		EquivalentResourceRegistry: runtime.NewEquivalentResourceRegistry(),
	}

	{
		storage, err := deploystorage.NewStorage(opts)
		utilruntime.Must(err)
		gversion.Storage["deployments"] = storage.Deployment

		groupVersions := []metav1.GroupVersionForDiscovery{
			metav1.GroupVersionForDiscovery{
				GroupVersion: apiappsv1.SchemeGroupVersion.String(),
				Version:      apiappsv1.SchemeGroupVersion.Version,
			},
		}
		apiGroup := metav1.APIGroup{
			Name:             apiappsv1.GroupName,
			Versions:         groupVersions,
			PreferredVersion: groupVersions[0],
		}
		discoveryManager.AddGroup(apiGroup)
		handler.GoRestfulContainer.Add(
			discovery.NewAPIGroupHandler(codecs, apiGroup).WebService())
	}

	_, _, err = gversion.InstallREST(handler.GoRestfulContainer)
	if err != nil {
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
