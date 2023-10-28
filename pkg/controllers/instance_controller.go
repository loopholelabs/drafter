package controllers

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	architectv1alpha1 "github.com/loopholelabs/architekt/pkg/api/k8s/v1alpha1"
	iclient "github.com/loopholelabs/architekt/pkg/client"
)

const (
	InstanceStateCreating   = "creating"
	InstanceStateMigrating  = "migrating"
	InstanceStateRecreating = "recreating"
	InstanceStateDeleting   = "deleting"
	InstanceStateRunning    = "running"

	instanceFinalizer = "io.loopholelabs.architekt/finalizer"
)

type InstanceReconciler struct {
	client   client.Client
	scheme   *runtime.Scheme
	recorder record.EventRecorder

	managerRESTClient iclient.ManagerRESTClient
}

func NewInstanceReconciler(
	client client.Client,
	scheme *runtime.Scheme,
	recorder record.EventRecorder,

	managerRESTClient iclient.ManagerRESTClient,
) *InstanceReconciler {
	return &InstanceReconciler{
		client:   client,
		scheme:   scheme,
		recorder: recorder,

		managerRESTClient: managerRESTClient,
	}
}

// See: https://book.kubebuilder.io/reference/markers.html
// +kubebuilder:rbac:groups=io.loopholelabs.architekt,resources=instances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=io.loopholelabs.architekt,resources=instances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=io.loopholelabs.architekt,resources=instances/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
func (r *InstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	instance := &architectv1alpha1.Instance{}
	if err := r.client.Get(ctx, req.NamespacedName, instance); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Could not find instance, skipping since object must have been deleted")

			return ctrl.Result{}, nil
		}

		log.Error(err, "Could not get instance, retrying")

		return ctrl.Result{}, err
	}

	if instance.Status.State == InstanceStateCreating ||
		instance.Status.State == InstanceStateMigrating ||
		instance.Status.State == InstanceStateRecreating ||
		instance.Status.State == InstanceStateDeleting {
		log.Info("An instance operation is already in progress, requeuing")

		return ctrl.Result{
			Requeue: true,
		}, nil
	}

	if !controllerutil.ContainsFinalizer(instance, instanceFinalizer) {
		if ok := controllerutil.AddFinalizer(instance, instanceFinalizer); !ok {
			log.Error(nil, "Could not add finalizer to instance")

			return ctrl.Result{Requeue: true}, nil
		}

		if err := r.client.Update(ctx, instance); err != nil {
			log.Error(err, "Could not update instance, retrying")

			return ctrl.Result{}, err
		}
	}

	if instance.GetDeletionTimestamp() != nil && controllerutil.ContainsFinalizer(instance, instanceFinalizer) {
		log.Info("Deleting instance")

		instance.Status.State = InstanceStateDeleting

		if err := r.client.Status().Update(ctx, instance); err != nil {
			log.Error(err, "Could not update instance status, retrying")

			return ctrl.Result{}, err
		}

		if err := r.managerRESTClient.DeleteInstance(instance.Status.NodeName, instance.Status.PackageLaddr); err != nil {
			log.Error(err, "Could not delete instance, retrying")

			return ctrl.Result{}, err
		}

		if ok := controllerutil.RemoveFinalizer(instance, instanceFinalizer); !ok {
			log.Error(nil, "Could not remove finalizer from instance")

			return ctrl.Result{Requeue: true}, nil
		}

		if err := r.client.Update(ctx, instance); err != nil {
			log.Error(err, "Could not update instance, retrying")

			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	if instance.Status.PackageLaddr == "" {
		log.Info("Creating instance")

		instance.Status.State = InstanceStateCreating

		if err := r.client.Status().Update(ctx, instance); err != nil {
			log.Error(err, "Could not update instance status, retrying")

			return ctrl.Result{}, err
		}

		newPackageLaddr, err := r.managerRESTClient.CreateInstance(instance.Spec.NodeName, instance.Spec.PackageRaddr)
		if err != nil {
			log.Error(err, "Could not create instance, retrying")

			return ctrl.Result{}, err
		}

		if err := r.client.Get(ctx, req.NamespacedName, instance); err != nil {
			log.Error(err, "Could not re-fetch instance, retrying")

			return ctrl.Result{}, err
		}

		instance.Status.PackageRaddr = instance.Spec.PackageRaddr
		instance.Status.NodeName = instance.Spec.NodeName
		instance.Status.PackageLaddr = newPackageLaddr
		instance.Status.State = InstanceStateRunning
		instance.Status.Message = ""

		if err := r.client.Status().Update(ctx, instance); err != nil {
			log.Error(err, "Could not update instance status, retrying")

			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	if instance.Status.PackageLaddr != "" && instance.Status.PackageRaddr == instance.Spec.PackageRaddr && instance.Status.NodeName != instance.Spec.NodeName {
		log.Info("Migrating instance")

		instance.Status.State = InstanceStateMigrating

		if err := r.client.Status().Update(ctx, instance); err != nil {
			log.Error(err, "Could not update instance status, retrying")

			return ctrl.Result{}, err
		}

		newPackageLaddr, err := r.managerRESTClient.CreateInstance(instance.Spec.NodeName, instance.Status.PackageLaddr)
		if err != nil {
			log.Error(err, "Could not create instance, retrying")

			return ctrl.Result{}, err
		}

		if err := r.client.Get(ctx, req.NamespacedName, instance); err != nil {
			log.Error(err, "Could not re-fetch instance, retrying")

			return ctrl.Result{}, err
		}

		instance.Status.PackageRaddr = instance.Spec.PackageRaddr
		instance.Status.NodeName = instance.Spec.NodeName
		instance.Status.PackageLaddr = newPackageLaddr
		instance.Status.State = InstanceStateRunning
		instance.Status.Message = ""

		if err := r.client.Status().Update(ctx, instance); err != nil {
			log.Error(err, "Could not update instance status, retrying")

			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	if instance.Status.PackageLaddr != "" && instance.Status.PackageRaddr != instance.Spec.PackageRaddr {
		log.Info("Recreating instance")

		instance.Status.State = InstanceStateRecreating

		if err := r.client.Status().Update(ctx, instance); err != nil {
			log.Error(err, "Could not update instance status, retrying")

			return ctrl.Result{}, err
		}

		oldNodeName := instance.Status.NodeName
		oldPackageLaddr := instance.Status.PackageLaddr

		newPackageLaddr, err := r.managerRESTClient.CreateInstance(instance.Spec.NodeName, instance.Spec.PackageRaddr)
		if err != nil {
			log.Error(err, "Could not create instance, retrying")

			return ctrl.Result{}, err
		}

		if err := r.client.Get(ctx, req.NamespacedName, instance); err != nil {
			log.Error(err, "Could not re-fetch instance, retrying")

			return ctrl.Result{}, err
		}

		instance.Status.PackageRaddr = instance.Spec.PackageRaddr
		instance.Status.NodeName = instance.Spec.NodeName
		instance.Status.PackageLaddr = newPackageLaddr

		if err := r.client.Status().Update(ctx, instance); err != nil {
			log.Error(err, "Could not update instance status, retrying")

			return ctrl.Result{}, err
		}

		// Delete the instance; if the new `packageRaddr` is a `packageLaddr` from an existing instance, this is a no-op since leeching an instance will lead to it being deleted automatically
		// This is however still necessary since the new `packageRaddr` can also be a package seeded from a registry, where this is not the case, and where the instance needs to be cleaned up manually here!
		if err := r.managerRESTClient.DeleteInstance(oldNodeName, oldPackageLaddr); err != nil {
			log.Info("Could not delete instance, ignoring", "error", err)
		}

		if err := r.client.Get(ctx, req.NamespacedName, instance); err != nil {
			log.Error(err, "Could not re-fetch instance, retrying")

			return ctrl.Result{}, err
		}

		instance.Status.State = InstanceStateRunning
		instance.Status.Message = ""

		if err := r.client.Status().Update(ctx, instance); err != nil {
			log.Error(err, "Could not update instance status, retrying")

			return ctrl.Result{}, err
		}

		log.Info("Instance is already in desired state, skipping")

		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

func (r *InstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&architectv1alpha1.Instance{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}
