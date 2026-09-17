package scheduler

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateDefinition(t *testing.T) {
	t.Parallel()
	valid := DefinitionInput{Code: "reconcile-orders", Name: "Reconcile orders", CronSpec: "0 */5 * * * *", Timezone: "Asia/Shanghai", Handler: "orders.reconcile", TimeoutSeconds: 30, LockTTLSeconds: 60, Status: "active", Payload: json.RawMessage(`{"batch_size":100}`)}
	require.NoError(t, validateDefinition(valid))

	tests := []struct {
		name   string
		change func(*DefinitionInput)
	}{
		{name: "invalid code", change: func(value *DefinitionInput) { value.Code = "Invalid code" }},
		{name: "invalid cron", change: func(value *DefinitionInput) { value.CronSpec = "not cron" }},
		{name: "unknown timezone", change: func(value *DefinitionInput) { value.Timezone = "Moon/Base" }},
		{name: "unregistered handler syntax", change: func(value *DefinitionInput) { value.Handler = "https://example.com" }},
		{name: "unbounded timeout", change: func(value *DefinitionInput) { value.TimeoutSeconds = 3601 }},
		{name: "array payload", change: func(value *DefinitionInput) { value.Payload = json.RawMessage(`[]`) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			test.change(&value)
			require.ErrorIs(t, validateDefinition(value), ErrInvalidDefinition)
		})
	}
}

func TestNormalizeDefinition(t *testing.T) {
	t.Parallel()
	got := normalizeDefinition(DefinitionInput{Code: " JOB-A ", Name: " Job A ", CronSpec: "0   * * * * *", Timezone: " Asia/Shanghai ", Handler: " Orders.Reconcile ", Status: "active"})
	require.Equal(t, "job-a", got.Code)
	require.Equal(t, "0 * * * * *", got.CronSpec)
	require.Equal(t, "orders.reconcile", got.Handler)
	require.JSONEq(t, `{}`, string(got.Payload))
}
