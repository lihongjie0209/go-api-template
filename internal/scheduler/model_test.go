package scheduler

import (
	"encoding/json"
	"strings"
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
		{name: "inline CRON_TZ conflicts with timezone field", change: func(value *DefinitionInput) { value.CronSpec = "CRON_TZ=UTC 0 */5 * * * *" }},
		{name: "inline TZ conflicts with timezone field", change: func(value *DefinitionInput) { value.CronSpec = "TZ=UTC 0 */5 * * * *" }},
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

func TestValidateDefinitionPayloadByteLimit(t *testing.T) {
	t.Parallel()
	input := DefinitionInput{Code: "payload-boundary", Name: "Payload boundary", CronSpec: "0 */5 * * * *", Timezone: "Asia/Shanghai", Handler: "system.sample", TimeoutSeconds: 30, LockTTLSeconds: 60, Status: "active"}
	input.Payload = json.RawMessage(`{"data":"` + strings.Repeat("x", (1<<20)-len(`{"data":""}`)) + `"}`)
	require.Len(t, input.Payload, 1<<20)
	require.NoError(t, validateDefinition(input))
	input.Payload = append(input.Payload[:len(input.Payload)-2], 'x', '"', '}')
	require.Len(t, input.Payload, (1<<20)+1)
	require.ErrorIs(t, validateDefinition(input), ErrInvalidDefinition)
}

func TestNormalizeDefinition(t *testing.T) {
	t.Parallel()
	got := normalizeDefinition(DefinitionInput{Code: " JOB-A ", Name: " Job A ", CronSpec: "0   * * * * *", Timezone: " Asia/Shanghai ", Handler: " Orders.Reconcile ", Status: "active"})
	require.Equal(t, "job-a", got.Code)
	require.Equal(t, "0 * * * * *", got.CronSpec)
	require.Equal(t, "orders.reconcile", got.Handler)
	require.JSONEq(t, `{}`, string(got.Payload))
}
