package controllers

import (
	"context"
	"github.com/go-logr/logr"
	xjoin "github.com/redhatinsights/xjoin-operator/api/v1alpha1"
	"github.com/redhatinsights/xjoin-operator/controllers/components"
	"github.com/redhatinsights/xjoin-operator/controllers/config"
	xjoinlogger "github.com/redhatinsights/xjoin-operator/controllers/log"
	"github.com/redhatinsights/xjoin-operator/controllers/parameters"
	"github.com/redhatinsights/xjoin-operator/controllers/utils"
	k8errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"time"
)

const xjoindatasourcepipelineFinalizer = "finalizer.xjoin.datasourcepipeline.cloud.redhat.com"

type XJoinDataSourcePipelineReconciler struct {
	Client    client.Client
	Log       logr.Logger
	Scheme    *runtime.Scheme
	Recorder  record.EventRecorder
	Namespace string
	Test      bool
}

func NewXJoinDataSourcePipelineReconciler(
	client client.Client,
	scheme *runtime.Scheme,
	log logr.Logger,
	recorder record.EventRecorder,
	namespace string,
	isTest bool) *XJoinDataSourcePipelineReconciler {

	return &XJoinDataSourcePipelineReconciler{
		Client:    client,
		Log:       log,
		Scheme:    scheme,
		Recorder:  recorder,
		Namespace: namespace,
		Test:      isTest,
	}
}

func (r *XJoinDataSourcePipelineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("xjoin-datasourcepipeline-controller").
		For(&xjoin.XJoinDataSourcePipeline{}).
		Complete(r)
}

// +kubebuilder:rbac:groups=xjoin.cloud.redhat.com,resources=xjoindatasourcepipelines;xjoindatasourcepipelines/status;xjoindatasourcepipelines/finalizers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kafka.strimzi.io,resources=kafkaconnectors;kafkaconnectors/finalizers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kafka.strimzi.io,resources=kafkatopics;kafkatopics/finalizers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kafka.strimzi.io,resources=kafkaconnects;kafkas,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps;pods,verbs=get;list;watch

func (r *XJoinDataSourcePipelineReconciler) Reconcile(ctx context.Context, request ctrl.Request) (result ctrl.Result, err error) {
	reqLogger := xjoinlogger.NewLogger("controller_xjoindatasourcepipeline", "DataSourcePipeline", request.Name, "Namespace", request.Namespace)
	reqLogger.Info("Reconciling XJoinDataSourcePipeline")

	instance, err := utils.FetchXJoinDataSourcePipeline(r.Client, request.NamespacedName, ctx)
	if err != nil {
		if k8errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return result, nil
		}
		// Error reading the object - requeue the request.
		return
	}

	p := parameters.BuildDataSourceParameters()

	configManager, err := config.NewManager(config.ManagerOptions{
		Client:         r.Client,
		Parameters:     p,
		ConfigMapNames: []string{"xjoin"},
		SecretNames:    nil,
		Namespace:      instance.Namespace,
		Spec:           instance.Spec,
		Context:        ctx,
	})
	if err != nil {
		return
	}
	err = configManager.Parse()
	if err != nil {
		return
	}

	if p.Pause.Bool() == true {
		reqLogger.Info("Resource is paused. Skipping reconcile loop")
		return
	}

	componentManager := components.NewComponentManager(p.Version.String())
	componentManager.AddComponent(components.NewAvroSchema("test.schema", p.AvroSchema.String()))
	componentManager.AddComponent(components.NewKafkaTopic())
	componentManager.AddComponent(components.NewDebeziumConnector())

	if instance.GetDeletionTimestamp() != nil {
		reqLogger.Info("Starting finalizer")
		err = componentManager.DeleteAll()
		if err != nil {
			return
		}

		controllerutil.RemoveFinalizer(instance, xjoindatasourcepipelineFinalizer)
		ctx, cancel := utils.DefaultContext()
		defer cancel()
		err = r.Client.Update(ctx, instance)
		if err != nil {
			return
		}

		reqLogger.Info("Successfully finalized")
		return reconcile.Result{}, nil
	}

	err = componentManager.CreateAll()
	if err != nil {
		return
	}

	return reconcile.Result{RequeueAfter: time.Second * 30}, nil
}