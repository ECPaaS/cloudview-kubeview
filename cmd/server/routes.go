// Default package
package main

//
// Route handlers for the API, does the actual k8s data scraping
// Ben Coleman, July 2019, v1
//

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strings"

	"github.com/benc-uk/go-rest-api/pkg/env"
	"github.com/gorilla/mux"

	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Config is simple structure returned by routeConfig
type Config struct {
	NamespaceScope string
}

// Data struct to hold our returned data
type scrapeData struct {
	Pods                   []apiv1.Pod                   `json:"pods"`
	Services               []apiv1.Service               `json:"services"`
	Endpoints              []apiv1.Endpoints             `json:"endpoints"`
	PersistentVolumes      []apiv1.PersistentVolume      `json:"persistentvolumes"`
	PersistentVolumeClaims []apiv1.PersistentVolumeClaim `json:"persistentvolumeclaims"`
	Deployments            []appsv1.Deployment           `json:"deployments"`
	DaemonSets             []appsv1.DaemonSet            `json:"daemonsets"`
	ReplicaSets            []appsv1.ReplicaSet           `json:"replicasets"`
	StatefulSets           []appsv1.StatefulSet          `json:"statefulsets"`
	Ingresses              []networkingv1.Ingress        `json:"ingresses"`
	ConfigMaps             []apiv1.ConfigMap             `json:"configmaps"`
	Secrets                []apiv1.Secret                `json:"secrets"`
}

// Redact any certificate data from the input byte slice
func redactCertificates(data []byte) []byte {
	certRegex := regexp.MustCompile(`(?i)-----+BEGIN\s+CERTIFICATE-----+[^\-]+-----+END\s+CERTIFICATE-----+`)
	return certRegex.ReplaceAll(data, []byte("__CERTIFICATE REDACTED__"))
}

// Redact certificates from any JSON-like structure recursively
func redactCertificatesInJSON(data interface{}) interface{} {
	switch v := data.(type) {
	case map[string]interface{}:
		for key, val := range v {
			v[key] = redactCertificatesInJSON(val)
		}
		return v
	case []interface{}:
		for i, val := range v {
			v[i] = redactCertificatesInJSON(val)
		}
		return v
	case string:
		return string(redactCertificates([]byte(v)))
	default:
		return data
	}
}

func redactSecrets(secrets []apiv1.Secret) []apiv1.Secret {
	for i, secret := range secrets {
		// Redact from secret data
		for key, value := range secret.Data {
			secret.Data[key] = redactCertificates(value)
		}

		// Redact from annotations, including kubectl.kubernetes.io/last-applied-configuration
		for key, value := range secret.Annotations {
			if key == "kubectl.kubernetes.io/last-applied-configuration" {
				// Redact sensitive data within the last-applied-configuration JSON
				secret.Annotations[key] = string(redactCertificates([]byte(value)))
			} else {
				// Redact certificates from other annotations as well
				secret.Annotations[key] = string(redactCertificates([]byte(value)))
			}
		}

		// Handle StringData field as well
		for key, value := range secret.StringData {
			redactedValue := redactCertificates([]byte(value))
			secret.StringData[key] = string(redactedValue)
		}

		// Reassign the redacted secret back to the slice
		secrets[i] = secret
	}
	return secrets
}

// Simple health check endpoint, returns 204 when healthy
func routeHealthCheck(resp http.ResponseWriter, req *http.Request) {
	if healthy {
		resp.WriteHeader(http.StatusNoContent)
		return
	}
	resp.WriteHeader(http.StatusServiceUnavailable)
}

// Return status information data
func routeStatus(resp http.ResponseWriter, req *http.Request) {
	type status struct {
		Healthy    bool   `json:"healthy"`
		Version    string `json:"version"`
		BuildInfo  string `json:"buildInfo"`
		Hostname   string `json:"hostname"`
		OS         string `json:"os"`
		Arch       string `json:"architecture"`
		CPU        int    `json:"cpuCount"`
		GoVersion  string `json:"goVersion"`
		ClientAddr string `json:"clientAddress"`
		ServerHost string `json:"serverHost"`
	}

	hostname, err := os.Hostname()
	if err != nil {
		hostname = "hostname not available"
	}

	currentStatus := status{
		Healthy:    healthy,
		Version:    version,
		BuildInfo:  buildInfo,
		Hostname:   hostname,
		GoVersion:  runtime.Version(),
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		CPU:        runtime.NumCPU(),
		ClientAddr: req.RemoteAddr,
		ServerHost: req.Host,
	}

	statusJSON, err := json.Marshal(currentStatus)
	if err != nil {
		http.Error(resp, "Failed to get status", http.StatusInternalServerError)
	}

	resp.Header().Add("Content-Type", "application/json")
	_, err = resp.Write(statusJSON)
	if err != nil {
		log.Println("Unable to write")
	}
}

// Return list of all namespaces in cluster
func routeGetNamespaces(resp http.ResponseWriter, req *http.Request) {
	ctx := context.Background()
	namespaces, err := clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Println("### Kubernetes API error", err.Error())
		http.Error(resp, err.Error(), http.StatusInternalServerError)
	}
	namespacesJSON, _ := json.Marshal(namespaces.Items)
	resp.Header().Set("Access-Control-Allow-Origin", "*")
	resp.Header().Add("Content-Type", "application/json")
	_, err = resp.Write(namespacesJSON)
	if err != nil {
		log.Println("Unable to write")
	}
}

// Return aggregated data from loads of different Kubernetes object types
func routeScrapeData(resp http.ResponseWriter, req *http.Request) {
	params := mux.Vars(req)
	namespace := params["ns"]

	ctx := context.Background()

	// Fetch Kubernetes resources
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Println("### Kubernetes API error:", err.Error())
		http.Error(resp, err.Error(), http.StatusInternalServerError)
		return
	}

	services, err := clientset.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Println("### Kubernetes API error:", err.Error())
		http.Error(resp, err.Error(), http.StatusInternalServerError)
		return
	}

	endpoints, err := clientset.CoreV1().Endpoints(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Println("### Kubernetes API error:", err.Error())
		http.Error(resp, err.Error(), http.StatusInternalServerError)
		return
	}

	pvs, err := clientset.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Println("### Kubernetes API error:", err.Error())
		http.Error(resp, err.Error(), http.StatusInternalServerError)
		return
	}

	pvcs, err := clientset.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Println("### Kubernetes API error:", err.Error())
		http.Error(resp, err.Error(), http.StatusInternalServerError)
		return
	}

	configmaps, err := clientset.CoreV1().ConfigMaps(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Println("### Kubernetes API error:", err.Error())
		http.Error(resp, err.Error(), http.StatusInternalServerError)
		return
	}

	secrets, err := clientset.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Println("### Kubernetes API error:", err.Error())
		http.Error(resp, err.Error(), http.StatusInternalServerError)
		return
	}

	deployments, err := clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Println("### Kubernetes API error:", err.Error())
		http.Error(resp, err.Error(), http.StatusInternalServerError)
		return
	}

	daemonsets, err := clientset.AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Println("### Kubernetes API error:", err.Error())
		http.Error(resp, err.Error(), http.StatusInternalServerError)
		return
	}

	replicasets, err := clientset.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Println("### Kubernetes API error:", err.Error())
		http.Error(resp, err.Error(), http.StatusInternalServerError)
		return
	}

	statefulsets, err := clientset.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Println("### Kubernetes API error:", err.Error())
		http.Error(resp, err.Error(), http.StatusInternalServerError)
		return
	}

	ingresses, err := clientset.NetworkingV1().Ingresses(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Println("### Kubernetes API error:", err.Error())
		http.Error(resp, err.Error(), http.StatusInternalServerError)
		return
	}

	// Remove and hide Helm v3 release secrets, we're never going to show them
	secrets.Items = filterSecrets(secrets.Items, func(v apiv1.Secret) bool {
		return !strings.HasPrefix(v.ObjectMeta.Name, "sh.helm.release")
	})

	// Redact sensitive data within secrets, including in kubectl.kubernetes.io/last-applied-configuration
	secrets.Items = redactSecrets(secrets.Items)

	// Redact any certificate data from ConfigMaps
	for _, configmap := range configmaps.Items {
		for key, value := range configmap.Data {
			configmap.Data[key] = string(redactCertificates([]byte(value)))
		}

		for key, value := range configmap.BinaryData {
			configmap.BinaryData[key] = redactCertificates(value)
		}
	}

	// Dump of results into the scrapeData struct
	scrapeResult := scrapeData{
		Pods:                   pods.Items,
		Services:               services.Items,
		Endpoints:              endpoints.Items,
		PersistentVolumes:      pvs.Items,
		PersistentVolumeClaims: pvcs.Items,
		Deployments:            deployments.Items,
		DaemonSets:             daemonsets.Items,
		ReplicaSets:            replicasets.Items,
		StatefulSets:           statefulsets.Items,
		Ingresses:              ingresses.Items,
		ConfigMaps:             configmaps.Items,
		Secrets:                secrets.Items,
	}

	// Marshal the results into JSON
	scrapeResultJSON, err := json.Marshal(scrapeResult)
	if err != nil {
		log.Println("### Failed to marshal scrape result: ", err)
		http.Error(resp, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Set headers and write response
	resp.Header().Set("Access-Control-Allow-Origin", "*")
	resp.Header().Add("Content-Type", "application/json")
	if _, err := resp.Write(scrapeResultJSON); err != nil {
		log.Println("Unable to write")
	}
}

// Simple config endpoint, returns NAMESPACE_SCOPE var to front end
func routeConfig(resp http.ResponseWriter, req *http.Request) {
	nsScope := env.GetEnvString("NAMESPACE_SCOPE", "*")
	conf := Config{NamespaceScope: nsScope}

	configJSON, _ := json.Marshal(conf)
	resp.Header().Set("Access-Control-Allow-Origin", "*")
	resp.Header().Add("Content-Type", "application/json")
	_, err := resp.Write([]byte(configJSON))
	if err != nil {
		log.Println("Unable to write")
	}
}

// Filter a slice of Secrets
func filterSecrets(secretList []apiv1.Secret, f func(apiv1.Secret) bool) []apiv1.Secret {
	newSlice := make([]apiv1.Secret, 0)
	for _, secret := range secretList {
		if f(secret) {
			newSlice = append(newSlice, secret)
		}
	}
	return newSlice
}
