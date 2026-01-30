package apply

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	LogInfo func(format string, args ...any)
)

func (a *apply) log(operation string, gvk schema.GroupVersionKind, obj kclient.Object, diff ...string) {
	if LogInfo == nil {
		return
	}
	extra := ""
	if len(diff) > 0 && diff[0] != "" {
		extra = " diff: " + diff[0]
	}
	if a.ensure {
		LogInfo("apply: %s [%s] [%s]%s", operation, logKey(obj), gvk, extra)
	} else {
		LogInfo("apply: %s [%s] [%s] by owner %s%s", operation, logKey(obj), gvk, a.ownerLogKey(), extra)
	}
}

func logKey(obj kclient.Object) string {
	ns, name := obj.GetNamespace(), obj.GetName()
	if ns == "" {
		return name
	}
	return ns + "/" + name
}

func (a *apply) ownerLogKey() string {
	var result strings.Builder
	if a.owner != nil {
		result.WriteString("[")
		result.WriteString(logKey(a.owner))
		result.WriteString("] [")
		result.WriteString(fmt.Sprint(a.ownerGVK))
		result.WriteString("]")
	}
	if a.ownerSubContext != "" {
		if result.Len() > 0 {
			result.WriteString(" ")
		}
		result.WriteString("subctx [")
		result.WriteString(a.ownerSubContext)
		result.WriteString("]")
	}
	return result.String()
}
