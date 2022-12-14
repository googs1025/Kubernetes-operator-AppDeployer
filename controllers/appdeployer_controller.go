/*
Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"errors"
	"fmt"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	deployv1 "operator-develop/api/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// AppDeployerReconciler reconciles a AppDeployer object
type AppDeployerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

var (
	oldSpecAnnotation = "old/Spec"
	configmapResourceVersion = ""
)

//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=deploy.jiang.operator,resources=appdeployers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=deploy.jiang.operator,resources=appdeployers/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=deploy.jiang.operator,resources=appdeployers/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the AppDeployer object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.2/pkg/reconcile
func (r *AppDeployerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logs := log.FromContext(ctx)

	logs.Info("Start Reconcile Loop")

	var appDeploy deployv1.AppDeployer
	err := r.Get(ctx, req.NamespacedName, &appDeploy)
	if err != nil {
		// ???????????????????????????????????????????????????queue??????
		// ?????????????????????????????????????????????not-found??????
		// ???????????????????????????queue(requeue)
		if client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}

		return ctrl.Result{Requeue: true}, nil
	}

	// 3. CreateOrUpdate ConfigMap
	var configmap corev1.ConfigMap
	configmap.Name = appDeploy.Name
	configmap.Namespace = appDeploy.Namespace
	if appDeploy.Spec.Configmap {

		mutateConfigmapRes, err := ctrl.CreateOrUpdate(ctx, r.Client, &configmap, func() error {
			// ?????????????????????
			MutateConfigmap(&appDeploy, &configmap)
			// ??????OwnerReference
			err := controllerutil.SetOwnerReference(&appDeploy, &configmap, r.Scheme)
			return err
		})
		if err != nil {
			return ctrl.Result{}, err
		}
		logs.Info("CreateOrUpdate", "Configmap", mutateConfigmapRes)
	} else {
		// ??????Configmap????????????????????????????????????
		err := r.Get(ctx, req.NamespacedName, &configmap)
		if err != nil {
			if client.IgnoreNotFound(err) != nil {
				logs.Info("not found Configmap resource")
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		}
		err = r.Delete(ctx, &configmap)
		if err != nil {
			if client.IgnoreNotFound(err) != nil {
				logs.Info("not found Configmap resource")
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		}
	}

	// ??????appDeploy?????????????????????deployment service (?????????????????????????????????)
	// ?????????????????????????????????????????????????????????
	// ?????? CreateOrUpdate

	// ????????????????????????????????????????????????????????????
	// 1. CreateOrUpdate Deployment
	var deployment appsv1.Deployment
	deployment.Name = appDeploy.Name
	deployment.Namespace = appDeploy.Namespace
	mutateDeploymentRes, err := ctrl.CreateOrUpdate(ctx, r.Client, &deployment, func() error {
		var needToChangeConfigmap bool
		if appDeploy.Spec.Configmap {
			var needConfigmap corev1.ConfigMap
			err := r.Get(ctx, req.NamespacedName, &needConfigmap)
			if err != nil {
				return err
			}
			fmt.Println("??????configmap:", needConfigmap.Name, needConfigmap.ResourceVersion)

			if needConfigmap.ResourceVersion != configmapResourceVersion {
				configmapResourceVersion = needConfigmap.ResourceVersion
				needToChangeConfigmap = true
			}
		}
		// ?????????????????????
		MutateDeployment(&appDeploy, &deployment, configmapResourceVersion, needToChangeConfigmap)
		// ??????OwnerReference
		err := controllerutil.SetOwnerReference(&appDeploy, &deployment, r.Scheme)
		return err
	})

	if err != nil {
		return ctrl.Result{}, err
	}

	logs.Info("CreateOrUpdate", "Deployment", mutateDeploymentRes)

	// 2. CreateOrUpdate Service
	var service corev1.Service
	service.Name = appDeploy.Name
	service.Namespace = appDeploy.Namespace
	// ????????????Service
	if appDeploy.Spec.Service {

		if !checkService(&appDeploy) {
			return ctrl.Result{}, errors.New("the ServiceType is ClusterIP, so NodePort shouldn't be set")
		}

		mutateServiceRes, err := ctrl.CreateOrUpdate(ctx, r.Client, &service, func() error {
			// ?????????????????????
			MutateService(&appDeploy, &service)
			// ??????OwnerReference
			err := controllerutil.SetOwnerReference(&appDeploy, &service, r.Scheme)
			return err
		})

		if err != nil {
			return ctrl.Result{}, err
		}

		logs.Info("CreateOrUpdate", "Service", mutateServiceRes)
	} else {
		// ??????Service????????????????????????????????????
		err := r.Get(ctx, req.NamespacedName, &service)
		if err != nil {
			if client.IgnoreNotFound(err) != nil {
				logs.Info("not found Service resource")
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		}
		err = r.Delete(ctx, &service)
		if err != nil {
			if client.IgnoreNotFound(err) != nil {
				logs.Info("not found Service resource")
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AppDeployerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&deployv1.AppDeployer{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Watches(&source.Kind{ // ???????????????
			Type: &appsv1.Deployment{},
		}, handler.Funcs{
			DeleteFunc: r.deploymentDeleteHandler,
		}).
		Watches(&source.Kind{ // ???????????????
			Type: &corev1.Service{},
		}, handler.Funcs{
			DeleteFunc: r.serviceDeleteHandler,
		}).
		Watches(&source.Kind{ // ???????????????
			Type: &corev1.ConfigMap{},
		}, handler.Funcs{
			DeleteFunc: r.configmapDeleteHandler,
		}).
		Complete(r)
}

func (r *AppDeployerReconciler) deploymentDeleteHandler(event event.DeleteEvent, limitingInterface workqueue.RateLimitingInterface) {
	for _, ref := range event.Object.GetOwnerReferences() {
		if ref.Kind == deployv1.Kind && ref.APIVersion == deployv1.ApiVersion {
			// ???????????????????????????pod????????????????????????loop?????????owerReference?????????????????????????????????pod???
			fmt.Println("???????????????????????????", event.Object.GetName(), event.Object.GetObjectKind())
			limitingInterface.Add(reconcile.Request{
				NamespacedName: types.NamespacedName{Name: ref.Name,
					Namespace: event.Object.GetNamespace()}})
		}
	}
}

func (r *AppDeployerReconciler) serviceDeleteHandler(event event.DeleteEvent, limitingInterface workqueue.RateLimitingInterface) {
	for _, ref := range event.Object.GetOwnerReferences() {
		if ref.Kind == deployv1.Kind && ref.APIVersion == deployv1.ApiVersion {
			// ???????????????????????????pod????????????????????????loop?????????owerReference?????????????????????????????????pod???
			fmt.Println("???????????????????????????", event.Object.GetName(), event.Object.GetObjectKind())
			limitingInterface.Add(reconcile.Request{
				NamespacedName: types.NamespacedName{Name: ref.Name,
					Namespace: event.Object.GetNamespace()}})
		}
	}
}

func (r *AppDeployerReconciler) configmapDeleteHandler(event event.DeleteEvent, limitingInterface workqueue.RateLimitingInterface) {
	for _, ref := range event.Object.GetOwnerReferences() {
		if ref.Kind == deployv1.Kind && ref.APIVersion == deployv1.ApiVersion {
			// ???????????????????????????pod????????????????????????loop?????????owerReference?????????????????????????????????pod???
			fmt.Println("???????????????????????????", event.Object.GetName(), event.Object.GetObjectKind())
			limitingInterface.Add(reconcile.Request{
				NamespacedName: types.NamespacedName{Name: ref.Name,
					Namespace: event.Object.GetNamespace()}})
		}
	}
}
