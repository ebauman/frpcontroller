package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	frpv1 "github.com/ebauman/frpcontroller/api/v1"
)

const (
	serviceOwnerKey = ".metadata.controller"
)

// ServiceReconciler reconciles a Service object
type ServiceReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=frp.1eb100.net,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=frp.1eb100.net,resources=services/status,verbs=get;update;patch

// Marking rbac settings for corev1 resources
// +kubebuilder:rbac:groups=core,resources=services,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=core,resources=pods,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=create;delete;get;list;patch;update;watch

func (r *ServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("service", req.NamespacedName)

	var service frpv1.Service
	err := r.Get(ctx, req.NamespacedName, &service)
	switch {
	case err == nil:
		return r.handleCreateOrUpdate(ctx, logger, &service)
	case apierrors.IsNotFound(err):
		return r.handleDeleted(ctx, logger, &service)
	default:
		logger.Error(err, "get service failed")

		return ctrl.Result{}, err
	}
}

func (r *ServiceReconciler) handleCreateOrUpdate(
	ctx context.Context,
	logger logr.Logger,
	service *frpv1.Service,
) (ctrl.Result, error) {
	endpointName := client.ObjectKey{
		Name:      service.Spec.Endpoint,
		Namespace: service.Namespace,
	}

	if service.Labels == nil {
		service.Labels = map[string]string{}
	}
	if v, exists := service.Labels[labelKeyEndpointName]; !exists || v != endpointName.Name {
		service.Labels[labelKeyEndpointName] = endpointName.Name
		if err := r.Update(ctx, service); err != nil {
			logger.Error(err, "update labels failed")
			return ctrl.Result{}, err
		}
	}

	var (
		kserviceList  corev1.ServiceList
		kserviceBound *corev1.Service
	)
	err := r.List(
		ctx, &kserviceList,
		client.InNamespace(service.Namespace),
		client.MatchingFields{serviceOwnerKey: service.Name},
	)
	if err != nil {
		logger.Error(err, "list services failed")
		return ctrl.Result{}, err
	}
	for _, kservice := range kserviceList.Items {
		kservice.Spec.Selector = service.Spec.Selector
		kservice.Spec.Ports = nil
		for _, port := range service.Spec.Ports {
			kservice.Spec.Ports = append(kservice.Spec.Ports, port.ToCorev1ServicePort())
		}
		if len(service.Spec.ServiceLabels) > 0 {
			// NOTE: reset all previous labels
			kservice.Labels = map[string]string{}
			for k, v := range service.Spec.ServiceLabels {
				kservice.Labels[k] = v
			}
		}
		err = r.Update(ctx, &kservice)
		if err != nil {
			logger.Error(err, fmt.Sprintf("update corev1.service %s failed", service.Name))
			return ctrl.Result{}, err
		}
		logger.Info(fmt.Sprintf("updated corev1.service: %s", kservice.Name))
		kserviceBound = &kservice
	}

	if kserviceBound == nil {
		var kservicePorts []corev1.ServicePort
		for _, port := range service.Spec.Ports {
			kservicePorts = append(kservicePorts, port.ToCorev1ServicePort())
		}
		kserviceBound = &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: fmt.Sprintf("%s-frpc-", service.Name),
				Namespace:    service.Namespace,
			},
			Spec: corev1.ServiceSpec{
				Type:     corev1.ServiceTypeClusterIP,
				Selector: service.Spec.Selector,
				Ports:    kservicePorts,
			},
		}
		err = ctrl.SetControllerReference(service, kserviceBound, r.Scheme)
		if err != nil {
			logger.Error(err, "set controller reference failed")
			return ctrl.Result{}, err
		}
		err = r.Create(ctx, kserviceBound)
		if err != nil {
			logger.Error(err, "create corev1.Service failed")
			return ctrl.Result{}, err
		}
		logger.Info(fmt.Sprintf("created service %s", kserviceBound.Name))
	}
	if kserviceBound.Spec.ClusterIP != "" {
		if service.Annotations == nil {
			service.Annotations = map[string]string{}
		}
		service.Annotations[annotationKeyServiceClusterIP] = kserviceBound.Spec.ClusterIP
		err = r.Update(ctx, service)
		if err != nil {
			logger.Error(err, "update service failed")
			return ctrl.Result{}, err
		}
		logger.Info(fmt.Sprintf(
			"binded cluster ip %s to service: %s",
			kserviceBound.Spec.ClusterIP, service.Name,
		))
	}

	var (
		endpoint        frpv1.Endpoint
		serviceNewState frpv1.ServiceState
	)
	err = r.Get(ctx, endpointName, &endpoint)
	switch {
	case err == nil:
		logger.Info(fmt.Sprintf("found endpoint %s (%s)", endpoint.Name, endpoint.Status.State))
		serviceNewState = frpv1.ServiceStateInactive
		if endpoint.Status.State == frpv1.EndpointConnected {
			serviceNewState = frpv1.ServiceStateActive
		}
	case apierrors.IsNotFound(err):
		logger.Info(fmt.Sprintf("endpoint %s does not exist, try later", endpointName.Name))

		serviceNewState = frpv1.ServiceStateInactive
	default:
		logger.Error(err, "get endpoint failed")
		return ctrl.Result{}, err
	}

	if serviceNewState != service.Status.State {
		service.Status.State = serviceNewState
		if err := r.Status().Update(ctx, service); err != nil {
			logger.Error(err, "update service status failed")
			return ctrl.Result{}, err
		}
		logger.Info(fmt.Sprintf("updated service status to: %s", service.Status.State))
	}

	switch service.Status.State {
	case frpv1.ServiceStateActive:
		return ctrl.Result{
			// NOTE: already active, requeue slower
			RequeueAfter: time.Duration(30) * time.Second,
		}, nil
	default:
		return ctrl.Result{
			RequeueAfter: time.Duration(10) * time.Second,
		}, nil
	}
}

func (r *ServiceReconciler) handleDeleted(
	ctx context.Context,
	logger logr.Logger,
	service *frpv1.Service,
) (ctrl.Result, error) {
	return ctrl.Result{}, nil
}

func (r *ServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	err := mgr.GetFieldIndexer().IndexField(
		context.Background(), &corev1.Service{}, serviceOwnerKey,
		func(rawObj client.Object) []string {
			kservice := rawObj.(*corev1.Service)
			owner := metav1.GetControllerOf(kservice)
			if owner == nil {
				return nil
			}
			if owner.APIVersion != apiGVStr || owner.Kind != KindService {
				return nil
			}
			return []string{owner.Name}
		},
	)
	if err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&frpv1.Service{}).
		Complete(r)
}
