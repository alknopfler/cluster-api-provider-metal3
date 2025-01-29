package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	bmov1alpha1 "github.com/metal3-io/baremetal-operator/apis/metal3.io/v1alpha1"
	infrav1 "github.com/metal3-io/cluster-api-provider-metal3/api/v1beta1"

	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/runtime"
	rest "k8s.io/client-go/rest"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func main() {
	// Set up logger
	debug := os.Getenv("DEBUG")
	logLevel := zapcore.InfoLevel // Default log level
	if debug == "true" {
		logLevel = zapcore.DebugLevel // Set log level to Debug if DEBUG=true
	}
	log.SetLogger(zap.New(zap.UseDevMode(true), zap.Level(logLevel)))

	// Create a Kubernetes client
	config, err := rest.InClusterConfig()
	setupLog := ctrl.Log.WithName("setup")
	if err != nil {
		setupLog.Error(err, "Error getting context kubeconfig")
	}

	setupLog.Info("Starting the Metal3 FKAS reconciler")

	// Add BareMetalHost to scheme
	scheme := runtime.NewScheme()
	if err := bmov1alpha1.AddToScheme(scheme); err != nil {
		setupLog.Error(err, "Error adding BareMetalHost to scheme")
	}

	setupLog.Info("----> passed baremetalhost to new scheme")
	if err := infrav1.AddToScheme(scheme); err != nil {
		setupLog.Error(err, "Error adding Metal3Machine to scheme")
	}

	setupLog.Info("----> passed metal3machine to new scheme")
	if err := clusterv1.AddToScheme(scheme); err != nil {
		setupLog.Error(err, "Error adding Machine to scheme")
	}

	setupLog.Info("----> passed machine to new scheme")
	// Create a Kubernetes client
	mgr, err := manager.New(config, manager.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "Error creating manager")
	}
	setupLog.Info("----> passed creating manager ")

	// Set up the BareMetalHost controller
	if err := ctrl.NewControllerManagedBy(mgr).
		For(&bmov1alpha1.BareMetalHost{}).
		Complete(reconcile.Func(func(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
			setupLog.Info("Detected change in BMH", "namespace", req.Namespace, "name", req.Name)
			bmh := &bmov1alpha1.BareMetalHost{}
			if err := mgr.GetClient().Get(ctx, req.NamespacedName, bmh); err != nil {
				setupLog.Info("----> ERROR error getClient get bmh: ", err.Error())
				setupLog.Error(err, "Error fetching BareMetalHost")
				return reconcile.Result{}, err
			}
			setupLog.Info("----> passed getClient get bmh: ")
			// Check if the state has changed to "provisioned"
			if bmh.Status.Provisioning.State != "provisioned" {
				setupLog.Info("----> ERROR error provisioning or provisioned state:", req.Name, req.Namespace)
				setupLog.V(4).Info(fmt.Sprintf("BMH %s/%s state is not in 'provisioning' or 'provisioned' state.", req.Namespace, req.Name))
				return reconcile.Result{}, nil
			}
			setupLog.Info("----> passed provisionign state != provisioned: ")
			uuid := bmh.ObjectMeta.UID
			setupLog.Info("----> VALUE UUID: ", uuid)

			if bmh.Spec.ConsumerRef == nil {
				return reconcile.Result{}, err
			}
			m3m := &infrav1.Metal3Machine{}
			m3mKey := client.ObjectKey{
				Namespace: bmh.Spec.ConsumerRef.Namespace,
				Name:      bmh.Spec.ConsumerRef.Name,
			}
			setupLog.Info("----> VALUE bmh.spec.consumerRef.namespace: ", m3mKey.Namespace)
			setupLog.Info("----> VALUE bmh.spec.consumerRef.name: ", m3mKey.Name)

			if err := mgr.GetClient().Get(ctx, m3mKey, m3m); err != nil {
				setupLog.Info("----> ERROR fetching Metal3Machine: ", err.Error())
				setupLog.Error(err, "Error fetching Metal3Machine", "namespace", bmh.Spec.ConsumerRef.Namespace, "name", bmh.Spec.ConsumerRef.Name)
				return reconcile.Result{}, err
			}
			setupLog.Info("----> passed fetching Metal3Machine: ")
			// Get the Machine object referenced by m3m.Metadata.OwnerReference.Name
			if len(m3m.OwnerReferences) == 0 {
				setupLog.Info("----> ERROR no owner reference found:", m3m.OwnerReferences)
				setupLog.Error(fmt.Errorf("no owner reference found"), "Metal3Machine has no owner reference")
				return reconcile.Result{}, fmt.Errorf("no owner reference found")
			}

			setupLog.Info("----> passed m3m.ownerreference")

			machineName := m3m.ObjectMeta.OwnerReferences[0].Name
			setupLog.Info("----> VALUE machineName: ", machineName)
			namespace := m3m.Namespace
			setupLog.Info("----> VALUE namespace: ", namespace)
			machine := &clusterv1.Machine{}
			machineKey := client.ObjectKey{
				Namespace: namespace,
				Name:      machineName,
			}
			setupLog.Info("----> VALUE machineKey: ", machineKey)
			if err := mgr.GetClient().Get(ctx, machineKey, machine); err != nil {
				setupLog.Info("----> ERROR fetching Machine: ", err.Error())
				setupLog.Error(err, "Error fetching Machine", "namespace", m3m.Namespace, "name", machineName)
				return reconcile.Result{}, err
			}
			setupLog.Info("----> passed fetching Machine: ")
			labels := machine.Labels
			setupLog.Info("----> VALUE machine.labels: ", labels)
			clusterName, ok := labels["cluster.x-k8s.io/cluster-name"]
			setupLog.Info("----> VALUE clusterName: ", clusterName)
			if !ok {
				return reconcile.Result{}, err
			}

			providerID := m3m.Spec.ProviderID
			setupLog.Info("----> VALUE providerID: ", providerID)
			url := "http://localhost:3333/updateNode"
			requestData := map[string]interface{}{
				"cluster":    clusterName,
				"nodeName":   machineName,
				"namespace":  namespace,
				"providerID": providerID,
				"uuid":       string(uuid),
				"labels":     labels,
				"k8sversion": machine.Spec.Version,
			}
			setupLog.Info("----> VALUE updateNode request data: ", requestData)
			jsonData, err := json.Marshal(requestData)
			if err != nil {
				setupLog.Error(err, "Error marshalling JSON")
				return reconcile.Result{}, err
			}
			setupLog.Info("----> passed marshal request data json ")
			setupLog.Info("Making PUT request", "content", string(jsonData))
			putReq, err := http.NewRequest(http.MethodPut, url, bytes.NewBuffer(jsonData))
			if err != nil {
				setupLog.Info("----> ERROR request put Update Node: ", err.Error())
				setupLog.Error(err, "Error creating PUT request")
				return reconcile.Result{}, err
			}
			setupLog.Info("----> passed request put Update Node: ")

			putReq.Header.Set("Content-Type", "application/json")
			client := &http.Client{}
			resp, err := client.Do(putReq)
			if err != nil {
				setupLog.Error(err, "Error making PUT request")
				return reconcile.Result{}, err
			}
			defer resp.Body.Close()
			setupLog.Info("----> resp.StatusCode: ", resp.StatusCode)
			if resp.StatusCode != http.StatusOK {
				setupLog.Info("----> ERROR PUT request failed with status: ", resp.Status)
				setupLog.Info(fmt.Sprintf("PUT request failed with status: %s", resp.Status))
				return reconcile.Result{}, fmt.Errorf("PUT request failed with status: %s", resp.Status)
			}
			setupLog.Info("----> passed PUT request failed with status: ")

			return reconcile.Result{}, nil
		})); err != nil {
		setupLog.Info("----> ERROR setting up controller: ", err.Error())
		setupLog.Error(err, "Error setting up controller")
	}

	// Start the manager
	setupLog.Info("Starting controller...")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Info("----> ERROR starting manager: ", err.Error())
		setupLog.Error(err, "Error starting manager")
	}
}
