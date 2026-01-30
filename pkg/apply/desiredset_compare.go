package apply

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/obot-platform/nah/pkg/data"
	"github.com/obot-platform/nah/pkg/log"
	"k8s.io/apimachinery/pkg/api/meta"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	LabelApplied = "apply.acorn.io/applied"
)

var (
	knownListKeys = map[string]bool{
		"apiVersion":    true,
		"containerPort": true,
		"devicePath":    true,
		"ip":            true,
		"kind":          true,
		"mountPath":     true,
		"name":          true,
		"port":          true,
		"topologyKey":   true,
		"type":          true,
	}
)

func prepareObjectForCreate(gvk schema.GroupVersionKind, obj kclient.Object, clone bool, rules []pathRule) (kclient.Object, error) {
	serialized, err := serializeApplied(obj, rules)
	if err != nil {
		return nil, err
	}

	if clone {
		obj = obj.DeepCopyObject().(kclient.Object)
		obj.SetUID("")
		obj.SetCreationTimestamp(v1.Time{})
	}
	m, err := meta.Accessor(obj)
	if err != nil {
		return nil, err
	}
	annotations := m.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}

	annotations[LabelApplied] = appliedToAnnotation(serialized)
	m.SetAnnotations(annotations)

	typed, err := meta.TypeAccessor(obj)
	if err != nil {
		return nil, err
	}

	apiVersion, kind := gvk.ToAPIVersionAndKind()
	typed.SetAPIVersion(apiVersion)
	typed.SetKind(kind)

	return obj, nil
}

func originalAndModified(gvk schema.GroupVersionKind, oldMetadata v1.Object, newObject kclient.Object, rules []pathRule) ([]byte, []byte, error) {
	original, err := getOriginalBytes(gvk, oldMetadata)
	if err != nil {
		return nil, nil, err
	}

	newObject, err = prepareObjectForCreate(gvk, newObject, true, rules)
	if err != nil {
		return nil, nil, err
	}

	modified, err := json.Marshal(newObject)
	return original, modified, err
}

func emptyMaps(data map[string]any, keys ...string) bool {
	for _, key := range append(keys, "__invalid_key__") {
		if len(data) == 0 {
			// map is empty so all children are empty too
			return true
		} else if len(data) > 1 {
			// map has more than one key so not empty
			return false
		}

		value, ok := data[key]
		if !ok {
			// map has one key but not what we are expecting so not considered empty
			return false
		}

		data, _ = value.(map[string]any)
	}

	return true
}

func mapField(data map[string]any, fields ...string) map[string]any {
	obj, _, _ := unstructured.NestedFieldNoCopy(data, fields...)
	v, _ := obj.(map[string]any)
	return v
}

func sanitizePatch(patch []byte, removeObjectSetAnnotation bool) ([]byte, error) {
	mod := false
	data := map[string]any{}
	err := json.Unmarshal(patch, &data)
	if err != nil {
		return nil, err
	}

	if _, ok := data["kind"]; ok {
		mod = true
		delete(data, "kind")
	}

	if _, ok := data["apiVersion"]; ok {
		mod = true
		delete(data, "apiVersion")
	}

	if _, ok := data["status"]; ok {
		mod = true
		delete(data, "status")
	}

	if deleted := removeMetadataFields(data); deleted {
		mod = true
	}

	if removeObjectSetAnnotation {
		metadata := mapField(data, "metadata")
		annotations := mapField(data, "metadata", "annotations")
		for k := range annotations {
			if strings.HasPrefix(k, LabelPrefix) {
				mod = true
				delete(annotations, k)
			}
		}
		if mod && len(annotations) == 0 {
			delete(metadata, "annotations")
			if len(metadata) == 0 {
				delete(data, "metadata")
			}
		}
	}

	if emptyMaps(data, "metadata", "annotations") {
		return []byte("{}"), nil
	}

	// If the only thing to update is the applied field then don't update
	if emptyMaps(data, "metadata", "annotations", "apply.acorn.io/applied") {
		return []byte("{}"), nil
	}

	if !mod {
		return patch, nil
	}

	return json.Marshal(data)
}

func (a *apply) applyPatch(gvk schema.GroupVersionKind, debugID string, oldObject, newObject kclient.Object) (bool, error) {
	rules := a.comparison.rulesFor(gvk)

	original, modified, err := originalAndModified(gvk, oldObject, newObject, rules)
	if err != nil {
		return false, err
	}

	current, err := json.Marshal(oldObject)
	if err != nil {
		return false, err
	}

	// Phase 1: Compare using hashed representations. The original (annotation)
	// bytes already contain hashes from serialization, so only modified and
	// current need hashing here.
	modifiedForCompare, err := applyHashRulesOnBytes(modified, rules)
	if err != nil {
		return false, fmt.Errorf("hash modified: %w", err)
	}
	currentForCompare, err := applyHashRulesOnBytes(current, rules)
	if err != nil {
		return false, fmt.Errorf("hash current: %w", err)
	}

	patchType, patch, err := createPatch(gvk, original, modifiedForCompare, currentForCompare)
	if err != nil {
		return false, fmt.Errorf("patch generation: %w", err)
	}

	if string(patch) == "{}" {
		return false, nil
	}

	patch, err = sanitizePatch(patch, false)
	if err != nil {
		return false, err
	}

	if string(patch) == "{}" {
		return false, nil
	}

	// Phase 2: If the patch contains hashed paths, a real change was detected
	// at those paths. Regenerate with real values by replacing hash markers in
	// the original with live cluster values, then recomputing the patch using
	// un-hashed modified and current.
	containsHashed, err := patchContainsHashedPaths(patch, rules)
	if err != nil {
		return false, fmt.Errorf("check hashed paths: %w", err)
	}
	if containsHashed {
		originalForApply, err := replaceHashedPathsWithCurrentValues(original, current, rules)
		if err != nil {
			return false, fmt.Errorf("replace hashed paths: %w", err)
		}
		patchType, patch, err = createPatch(gvk, originalForApply, modified, current)
		if err != nil {
			return false, fmt.Errorf("patch generation (phase 2): %w", err)
		}
		if string(patch) == "{}" {
			return false, nil
		}
		patch, err = sanitizePatch(patch, false)
		if err != nil {
			return false, err
		}
		if string(patch) == "{}" {
			return false, nil
		}
	}

	log.Debugf("DesiredSet - Patch %s %s/%s for %s -- [PATCH:%s, ORIGINAL:%s, MODIFIED:%s, CURRENT:%s]", gvk, oldObject.GetNamespace(), oldObject.GetName(), debugID, patch, original, modified, current)
	reconciler := a.reconcilers[gvk]
	if reconciler != nil {
		newObject, err := prepareObjectForCreate(gvk, newObject, true, rules)
		if err != nil {
			return false, err
		}
		originalObject, err := getOriginalObject(gvk, oldObject)
		if err != nil {
			return false, err
		}
		if originalObject == nil {
			originalObject = oldObject
		}
		handled, err := reconciler(originalObject, newObject)
		if err != nil {
			return false, err
		}
		if handled {
			return true, nil
		}
	}

	ustr := &unstructured.Unstructured{}
	ustr.SetResourceVersion(oldObject.GetResourceVersion())
	ustr.SetGroupVersionKind(gvk)
	ustr.SetNamespace(oldObject.GetNamespace())
	ustr.SetName(oldObject.GetName())

	log.Debugf("DesiredSet - Updated %s %s/%s for %s -- %s %s", gvk, oldObject.GetNamespace(), oldObject.GetName(), debugID, patchType, patch)
	a.log("patching", gvk, oldObject, string(patch))
	if a.ensure {
		newObject.SetResourceVersion(oldObject.GetResourceVersion())
		return true, a.client.Patch(a.ctx, newObject, kclient.RawPatch(patchType, patch))
	}
	return true, a.client.Patch(a.ctx, ustr, kclient.RawPatch(patchType, patch))
}

func (a *apply) compareObjects(gvk schema.GroupVersionKind, debugID string, oldObject, newObject kclient.Object) error {
	if ran, err := a.applyPatch(gvk, debugID, oldObject, newObject); err != nil {
		return err
	} else if !ran {
		if a.ensure {
			srcObject := oldObject.DeepCopyObject()
			dstVal := reflect.ValueOf(newObject)
			srcVal := reflect.ValueOf(srcObject)
			if !srcVal.Type().AssignableTo(dstVal.Type()) {
				return fmt.Errorf("type %s not assignable to %s", srcVal.Type(), dstVal.Type())
			}
			reflect.Indirect(dstVal).Set(reflect.Indirect(srcVal))
		}
		log.Debugf("DesiredSet - No change(2) %s %s/%s for %s", gvk, oldObject.GetNamespace(), oldObject.GetName(), debugID)
	}

	return nil
}

func removeMetadataFields(data map[string]any) bool {
	metadata, ok := data["metadata"]
	if !ok {
		return false
	}

	mod := false
	data, _ = metadata.(map[string]any)
	for _, key := range []string{"creationTimestamp", "generation", "resourceVersion", "uid", "managedFields"} {
		if _, ok := data[key]; ok {
			delete(data, key)
			mod = true
		}
	}

	return mod
}

func getOriginalObject(gvk schema.GroupVersionKind, obj v1.Object) (kclient.Object, error) {
	original := appliedFromAnnotation(obj.GetAnnotations()[LabelApplied])
	if len(original) == 0 {
		return nil, nil
	}

	mapObj := map[string]any{}
	err := json.Unmarshal(original, &mapObj)
	if err != nil {
		return nil, err
	}

	removeMetadataFields(mapObj)
	return prepareObjectForCreate(gvk, &unstructured.Unstructured{
		Object: mapObj,
	}, true, nil)
}

func getOriginalBytes(gvk schema.GroupVersionKind, obj v1.Object) ([]byte, error) {
	objCopy, err := getOriginalObject(gvk, obj)
	if err != nil {
		return nil, err
	}
	if objCopy == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(objCopy)
}

func appliedFromAnnotation(str string) []byte {
	if len(str) == 0 || str[0] == '{' {
		return []byte(str)
	}

	b, err := base64.RawStdEncoding.DecodeString(str)
	if err != nil {
		return nil
	}

	r, err := gzip.NewReader(bytes.NewBuffer(b))
	if err != nil {
		return nil
	}

	b, err = io.ReadAll(r)
	if err != nil {
		return nil
	}

	return b
}

func pruneList(data []any) []any {
	result := make([]any, 0, len(data))
	for _, v := range data {
		switch typed := v.(type) {
		case map[string]any:
			result = append(result, pruneValues(typed, true))
		case []any:
			result = append(result, pruneList(typed))
		default:
			result = append(result, v)
		}
	}
	return result
}

func pruneValues(data map[string]any, isList bool) map[string]any {
	result := map[string]any{}
	for k, v := range data {
		switch typed := v.(type) {
		case map[string]any:
			result[k] = pruneValues(typed, false)
		case []any:
			result[k] = pruneList(typed)
		default:
			if isList && knownListKeys[k] {
				result[k] = v
			} else {
				switch x := v.(type) {
				case string:
					if len(x) > 64 {
						sum := sha256.Sum256([]byte(x))
						result[k] = x[:64] + hex.EncodeToString(sum[:])[:8]
					} else {
						result[k] = v
					}
				case []byte:
					result[k] = nil
				default:
					result[k] = v
				}
			}
		}
	}
	return result
}

func serializeApplied(obj kclient.Object, rules []pathRule) ([]byte, error) {
	data, err := data.ToMapInterface(obj)
	if err != nil {
		return nil, err
	}

	// Deep-copy data before pruning so we can restore NoPrune paths.
	var originalData map[string]any
	if len(rules) > 0 {
		b, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &originalData); err != nil {
			return nil, err
		}
	}

	data = pruneValues(data, false)

	if len(rules) > 0 {
		if err := applyNoPruneRules(data, originalData, rules); err != nil {
			return nil, err
		}
		// Compute hashes from the un-pruned originalData so that the stored
		// hashes match what applyHashRulesOnBytes produces on full object
		// bodies during comparison. This avoids Phase 1 false positives.
		if err := applyHashRules(originalData, rules); err != nil {
			return nil, err
		}
		for _, r := range rules {
			if r.strategy != ComparisonStrategyHash {
				continue
			}
			if val, ok := getNestedValue(originalData, r.segments); ok {
				setNestedValue(data, r.segments, val)
			}
		}
	}

	return json.Marshal(data)
}

func appliedToAnnotation(b []byte) string {
	buf := &bytes.Buffer{}
	w := gzip.NewWriter(buf)
	if _, err := w.Write(b); err != nil {
		return string(b)
	}
	if err := w.Close(); err != nil {
		return string(b)
	}
	return base64.RawStdEncoding.EncodeToString(buf.Bytes())
}

// createPatch is adapted from "kubectl apply"
func createPatch(gvk schema.GroupVersionKind, original, modified, current []byte) (types.PatchType, []byte, error) {
	var (
		patchType types.PatchType
		patch     []byte
	)

	patchType, lookupPatchMeta, err := getMergeStyle(gvk)
	if err != nil {
		return patchType, nil, err
	}

	if patchType == types.StrategicMergePatchType {
		patch, err = strategicpatch.CreateThreeWayMergePatch(original, modified, current, lookupPatchMeta, true)
	} else {
		patch, err = jsonmergepatch.CreateThreeWayJSONMergePatch(original, modified, current)
	}

	if err != nil {
		log.Errorf("Failed to calcuated patch: %v", err)
	}

	return patchType, patch, err
}
