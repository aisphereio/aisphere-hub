package data

import (
	"context"
	"errors"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/json"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/kernel/kubernetesx"
)

// ---- GVR definitions for Agent Sandbox CRDs ----
//
// The upstream agent-sandbox operator (kubernetes-sigs/agent-sandbox v0.5.2)
// registers four CRDs. Hub never imports the upstream Go types; instead it
// uses unstructured.Unstructured + the controller-runtime client's
// ApplyUnstructured method (SSA) so that CRD type changes do not affect Hub.

var (
	sandboxGVR = schema.GroupVersionResource{
		Group: "agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxes",
	}
	sandboxTemplateGVR = schema.GroupVersionResource{
		Group: "extensions.agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxtemplates",
	}
	warmPoolGVR = schema.GroupVersionResource{
		Group: "extensions.agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxwarmpools",
	}
	sandboxClaimGVR = schema.GroupVersionResource{
		Group: "extensions.agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxclaims",
	}
)

// ---- SandboxTemplate operations ----

func (p *k8sClientPool) ApplySandboxTemplate(ctx context.Context, clusterID string, locator biz.CredentialLocator, spec biz.SandboxTemplateApplySpec) error {
	client, err := p.getOrBuild(ctx, clusterID, locator, kubernetesx.Credential{})
	if err != nil {
		return err
	}
	ns := spec.Namespace
	if ns == "" {
		ns = "agent-sandbox-system"
	}
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "extensions.agents.x-k8s.io/v1beta1",
		"kind":       "SandboxTemplate",
		"metadata": map[string]interface{}{
			"name":      spec.Name,
			"namespace": ns,
			"labels":    spec.Labels,
		},
		"spec": map[string]interface{}{
			"podTemplate": map[string]interface{}{
				"spec": map[string]interface{}{
					"containers": []interface{}{
						map[string]interface{}{
							"name":    "runtime",
							"image":   spec.Image,
							"command": spec.ContainerCommand,
						},
					},
					"restartPolicy": "OnFailure",
				},
			},
		},
	}}
	obj.SetAPIVersion("extensions.agents.x-k8s.io/v1beta1")
	obj.SetKind("SandboxTemplate")
	return client.ApplyUnstructured(ctx, obj, kubernetesx.ApplyOptions{FieldManager: "aisphere-hub-sandbox-template"})
}

func (p *k8sClientPool) DeleteSandboxTemplate(ctx context.Context, clusterID string, locator biz.CredentialLocator, namespace, kubeName string) error {
	client, err := p.getOrBuild(ctx, clusterID, locator, kubernetesx.Credential{})
	if err != nil {
		return err
	}
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "extensions.agents.x-k8s.io", Version: "v1beta1", Kind: "SandboxTemplate",
	})
	obj.SetName(kubeName)
	obj.SetNamespace(namespace)
	return client.Delete(ctx, obj)
}

func (p *k8sClientPool) ListSandboxTemplates(ctx context.Context, clusterID string, locator biz.CredentialLocator, namespace string) ([]biz.SandboxTemplateSyncResult, error) {
	client, err := p.getOrBuild(ctx, clusterID, locator, kubernetesx.Credential{})
	if err != nil {
		return nil, err
	}
	dyn := client.Dynamic()
	if dyn == nil {
		return nil, errors.New("dynamic client not available")
	}
	list, err := dyn.Resource(sandboxTemplateGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list sandboxtemplates: %w", err)
	}
	out := make([]biz.SandboxTemplateSyncResult, 0, len(list.Items))
	for _, item := range list.Items {
		image := ""
		if containers, found, _ := unstructured.NestedSlice(item.Object, "spec", "podTemplate", "spec", "containers"); found && len(containers) > 0 {
			if c, ok := containers[0].(map[string]interface{}); ok {
				if v, ok := c["image"].(string); ok {
					image = v
				}
			}
		}
		out = append(out, biz.SandboxTemplateSyncResult{
			Name:            item.GetName(),
			Namespace:       item.GetNamespace(),
			UID:             string(item.GetUID()),
			ResourceVersion: item.GetResourceVersion(),
			Image:           image,
			Labels:          item.GetLabels(),
		})
	}
	return out, nil
}

// ---- Sandbox operations ----

func (p *k8sClientPool) ApplySandbox(ctx context.Context, clusterID string, locator biz.CredentialLocator, spec biz.SandboxApplySpec) error {
	client, err := p.getOrBuild(ctx, clusterID, locator, kubernetesx.Credential{})
	if err != nil {
		return err
	}
	operatingMode := spec.OperatingMode
	if operatingMode == "" {
		operatingMode = biz.SandboxOperatingModeRunning
	}
	// K8s Sandbox CRD expects TitleCase ("Running"/"Suspended"); the biz layer
	// stores UPPERCASE to match the DB CHECK constraint. Normalize here.
	switch strings.ToUpper(operatingMode) {
	case "SUSPENDED":
		operatingMode = "Suspended"
	default:
		operatingMode = "Running"
	}
	// Build the inline podTemplate (the Sandbox CRD has no sandboxTemplateRef
	// field — podTemplate.spec is required). The image + command come from the
	// Hub SandboxTemplate record, inlined by the biz layer.
	container := map[string]interface{}{
		"name":  "runtime",
		"image": spec.Image,
	}
	if len(spec.ContainerCommand) > 0 {
		cmds := make([]interface{}, len(spec.ContainerCommand))
		for i, c := range spec.ContainerCommand {
			cmds[i] = c
		}
		container["command"] = cmds
	}
	podSpec := map[string]interface{}{
		"containers":     []interface{}{container},
		"restartPolicy":  "OnFailure",
	}
	specMap := map[string]interface{}{
		"operatingMode": operatingMode,
		"podTemplate": map[string]interface{}{
			"spec": podSpec,
		},
	}
	// Annotate the source template for traceability (not a spec field).
	annotations := map[string]interface{}{}
	if spec.TemplateRef != "" {
		annotations["agents.x-k8s.io/sandbox-template-ref"] = spec.TemplateRef
	}
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1beta1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name":        spec.Name,
			"namespace":   spec.Namespace,
			"labels":      spec.Labels,
			"annotations": annotations,
		},
		"spec": specMap,
	}}
	obj.SetAPIVersion("agents.x-k8s.io/v1beta1")
	obj.SetKind("Sandbox")
	return client.ApplyUnstructured(ctx, obj, kubernetesx.ApplyOptions{FieldManager: "aisphere-hub-sandbox"})
}

func (p *k8sClientPool) DeleteSandbox(ctx context.Context, clusterID string, locator biz.CredentialLocator, namespace, kubeName string) error {
	client, err := p.getOrBuild(ctx, clusterID, locator, kubernetesx.Credential{})
	if err != nil {
		return err
	}
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "agents.x-k8s.io", Version: "v1beta1", Kind: "Sandbox",
	})
	obj.SetName(kubeName)
	obj.SetNamespace(namespace)
	return client.Delete(ctx, obj)
}

func (p *k8sClientPool) ListSandboxes(ctx context.Context, clusterID string, locator biz.CredentialLocator, namespace string) ([]biz.SandboxSyncResult, error) {
	client, err := p.getOrBuild(ctx, clusterID, locator, kubernetesx.Credential{})
	if err != nil {
		return nil, err
	}
	dyn := client.Dynamic()
	if dyn == nil {
		return nil, errors.New("dynamic client not available")
	}
	list, err := dyn.Resource(sandboxGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list sandboxes: %w", err)
	}
	out := make([]biz.SandboxSyncResult, 0, len(list.Items))
	for _, item := range list.Items {
		result := biz.SandboxSyncResult{
			Name:            item.GetName(),
			Namespace:       item.GetNamespace(),
			UID:             string(item.GetUID()),
			ResourceVersion: item.GetResourceVersion(),
			Labels:          item.GetLabels(),
		}
		// Extract status fields.
		result.Phase = sandboxReadyReason(item.Object)
		result.PodName, _, _ = unstructured.NestedString(item.Object, "status", "podName")
		result.PodIP, _, _ = unstructured.NestedString(item.Object, "status", "podIP")
		result.NodeName, _, _ = unstructured.NestedString(item.Object, "status", "nodeName")
		// Image from spec.sandboxTemplateRef is not in the Sandbox itself; try
		// status.image if the operator populates it, else leave empty.
		result.Image, _, _ = unstructured.NestedString(item.Object, "status", "image")
		out = append(out, result)
	}
	return out, nil
}

func (p *k8sClientPool) GetSandboxStatus(ctx context.Context, clusterID string, locator biz.CredentialLocator, namespace, kubeName string) (biz.SandboxRuntimeStatus, error) {
	client, err := p.getOrBuild(ctx, clusterID, locator, kubernetesx.Credential{})
	if err != nil {
		return biz.SandboxRuntimeStatus{}, err
	}
	dyn := client.Dynamic()
	if dyn == nil {
		return biz.SandboxRuntimeStatus{}, errors.New("dynamic client not available")
	}
	obj, err := dyn.Resource(sandboxGVR).Namespace(namespace).Get(ctx, kubeName, metav1.GetOptions{})
	if err != nil {
		return biz.SandboxRuntimeStatus{}, fmt.Errorf("get sandbox %s: %w", kubeName, err)
	}
	status := biz.SandboxRuntimeStatus{
		Name:          obj.GetName(),
		Namespace:     obj.GetNamespace(),
		Phase:         sandboxReadyReason(obj.Object),
		PodName:       getNestedString(obj.Object, "status", "podName"),
		PodIP:         getNestedString(obj.Object, "status", "podIP"),
		NodeName:      getNestedString(obj.Object, "status", "nodeName"),
		Image:         getNestedString(obj.Object, "status", "image"),
		OperatingMode: getNestedString(obj.Object, "spec", "operatingMode"),
	}
	return status, nil
}

// ---- WarmPool operations ----

func (p *k8sClientPool) ApplyWarmPool(ctx context.Context, clusterID string, locator biz.CredentialLocator, spec biz.WarmPoolApplySpec) error {
	client, err := p.getOrBuild(ctx, clusterID, locator, kubernetesx.Credential{})
	if err != nil {
		return err
	}
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "extensions.agents.x-k8s.io/v1beta1",
		"kind":       "SandboxWarmPool",
		"metadata": map[string]interface{}{
			"name":      spec.Name,
			"namespace": spec.Namespace,
		},
		"spec": map[string]interface{}{
			"replicas": spec.Replicas,
			"sandboxTemplateRef": map[string]interface{}{
				"name": spec.TemplateRef,
			},
		},
	}}
	obj.SetAPIVersion("extensions.agents.x-k8s.io/v1beta1")
	obj.SetKind("SandboxWarmPool")
	return client.ApplyUnstructured(ctx, obj, kubernetesx.ApplyOptions{FieldManager: "aisphere-hub-warm-pool"})
}

func (p *k8sClientPool) DeleteWarmPool(ctx context.Context, clusterID string, locator biz.CredentialLocator, namespace, kubeName string) error {
	client, err := p.getOrBuild(ctx, clusterID, locator, kubernetesx.Credential{})
	if err != nil {
		return err
	}
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "extensions.agents.x-k8s.io", Version: "v1beta1", Kind: "SandboxWarmPool",
	})
	obj.SetName(kubeName)
	obj.SetNamespace(namespace)
	return client.Delete(ctx, obj)
}

func (p *k8sClientPool) ListWarmPools(ctx context.Context, clusterID string, locator biz.CredentialLocator, namespace string) ([]biz.WarmPoolSyncResult, error) {
	client, err := p.getOrBuild(ctx, clusterID, locator, kubernetesx.Credential{})
	if err != nil {
		return nil, err
	}
	dyn := client.Dynamic()
	if dyn == nil {
		return nil, errors.New("dynamic client not available")
	}
	list, err := dyn.Resource(warmPoolGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list warm pools: %w", err)
	}
	out := make([]biz.WarmPoolSyncResult, 0, len(list.Items))
	for _, item := range list.Items {
		replicas, _, _ := unstructured.NestedInt64(item.Object, "spec", "replicas")
		readyReplicas, _, _ := unstructured.NestedInt64(item.Object, "status", "readyReplicas")
		templateRef, _, _ := unstructured.NestedString(item.Object, "spec", "sandboxTemplateRef", "name")
		out = append(out, biz.WarmPoolSyncResult{
			Name:            item.GetName(),
			Namespace:       item.GetNamespace(),
			UID:             string(item.GetUID()),
			ResourceVersion: item.GetResourceVersion(),
			TemplateRef:     templateRef,
			Replicas:        int32(replicas),
			ReadyReplicas:   int32(readyReplicas),
		})
	}
	return out, nil
}

// ---- SandboxClaim operations ----

func (p *k8sClientPool) ApplySandboxClaim(ctx context.Context, clusterID string, locator biz.CredentialLocator, spec biz.SandboxClaimApplySpec) error {
	client, err := p.getOrBuild(ctx, clusterID, locator, kubernetesx.Credential{})
	if err != nil {
		return err
	}
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "extensions.agents.x-k8s.io/v1beta1",
		"kind":       "SandboxClaim",
		"metadata": map[string]interface{}{
			"name":      spec.Name,
			"namespace": spec.Namespace,
		},
		"spec": map[string]interface{}{
			"warmPoolRef": map[string]interface{}{
				"name": spec.WarmPoolRef,
			},
		},
	}}
	obj.SetAPIVersion("extensions.agents.x-k8s.io/v1beta1")
	obj.SetKind("SandboxClaim")
	return client.ApplyUnstructured(ctx, obj, kubernetesx.ApplyOptions{FieldManager: "aisphere-hub-sandbox-claim"})
}

func (p *k8sClientPool) DeleteSandboxClaim(ctx context.Context, clusterID string, locator biz.CredentialLocator, namespace, kubeName string) error {
	client, err := p.getOrBuild(ctx, clusterID, locator, kubernetesx.Credential{})
	if err != nil {
		return err
	}
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "extensions.agents.x-k8s.io", Version: "v1beta1", Kind: "SandboxClaim",
	})
	obj.SetName(kubeName)
	obj.SetNamespace(namespace)
	return client.Delete(ctx, obj)
}

func (p *k8sClientPool) ListSandboxClaims(ctx context.Context, clusterID string, locator biz.CredentialLocator, namespace string) ([]biz.SandboxClaimSyncResult, error) {
	client, err := p.getOrBuild(ctx, clusterID, locator, kubernetesx.Credential{})
	if err != nil {
		return nil, err
	}
	dyn := client.Dynamic()
	if dyn == nil {
		return nil, errors.New("dynamic client not available")
	}
	list, err := dyn.Resource(sandboxClaimGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list sandbox claims: %w", err)
	}
	out := make([]biz.SandboxClaimSyncResult, 0, len(list.Items))
	for _, item := range list.Items {
		// spec.warmPoolRef.name → WarmPoolRef.
		warmPoolRef := getNestedString(item.Object, "spec", "warmPoolRef", "name")
		// status.sandbox.name → SandboxName (empty until the operator resolves
		// the claim to a concrete Sandbox).
		sandboxName := getNestedString(item.Object, "status", "sandbox", "name")
		// status.sandbox.podIPs is a list; take the first element.
		var sandboxPodIP string
		if ips, found, _ := unstructured.NestedStringSlice(item.Object, "status", "sandbox", "podIPs"); found && len(ips) > 0 {
			sandboxPodIP = ips[0]
		}
		// Ready reflects the Ready condition with status "True". sandboxReadyReason
		// returns the condition's reason when present (e.g. "DependenciesReady")
		// or "Ready" when the status is True without an explicit reason.
		reason := sandboxReadyReason(item.Object)
		ready := reason == "DependenciesReady" || reason == "Ready"
		out = append(out, biz.SandboxClaimSyncResult{
			Name:            item.GetName(),
			Namespace:       item.GetNamespace(),
			UID:             string(item.GetUID()),
			ResourceVersion: item.GetResourceVersion(),
			WarmPoolRef:     warmPoolRef,
			SandboxName:     sandboxName,
			SandboxPodIP:    sandboxPodIP,
			Ready:           ready,
		})
	}
	return out, nil
}

// ---- Helpers ----

// sandboxReadyReason extracts the Ready condition reason from a Sandbox CRD's
// status.conditions. Returns "Unknown" if conditions are absent.
func sandboxReadyReason(obj map[string]interface{}) string {
	conditions, found, _ := unstructured.NestedSlice(obj, "status", "conditions")
	if !found {
		return "Unknown"
	}
	for _, c := range conditions {
		cond, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := cond["type"].(string); t == "Ready" {
			if reason, ok := cond["reason"].(string); ok {
				return reason
			}
			if status, ok := cond["status"].(string); ok {
				if status == "True" {
					return "Ready"
				}
				return "NotReady"
			}
		}
	}
	return "Unknown"
}

func getNestedString(obj map[string]interface{}, fields ...string) string {
	v, _, _ := unstructured.NestedString(obj, fields...)
	return v
}

// ensure json import is used (for potential future status marshaling).
var _ = json.Marshal

// Compile-time assertion that k8sClientPool implements SandboxProvider.
var _ biz.SandboxProvider = (*k8sClientPool)(nil)
