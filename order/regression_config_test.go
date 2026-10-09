package order

import (
	"reflect"
	"verbose-fortnight/config"
)

func setReconcileTimeout(cfg *config.Config) {
	v := reflect.ValueOf(cfg).Elem().FieldByName("OrderReconcileTimeoutMs")
	if v.IsValid() {
		v.SetInt(40)
	}
}
