package pbac

// PlatformResourceDefinitions contains the first platform authorization
// vocabulary. Resource keys are stable API contracts; changing a key requires
// migrating persisted policies.
func PlatformResourceDefinitions() ResourceDefinitions {
	return ResourceDefinitions{
		resource("identity.user", "用户", ResourceScopePlatform, "create", "read", "list", "update", "delete", "reset-password", "force-logout"),
		resource("identity.profile", "当前用户资料", ResourceScopePrincipal, "read"),
		resource("identity.session", "当前用户登录会话", ResourceScopePrincipal, "list", "revoke", "logout"),
		resource("identity.credential", "当前用户凭据", ResourceScopePrincipal, "change-password"),
		resource("authorization.capability", "当前用户前端能力", ResourceScopePrincipal, "evaluate"),
		resource("identity.service-account", "服务账号", ResourceScopePlatform, "create", "read", "list", "update", "rotate-secret", "delete"),
		resource("identity.internal-authentication", "内部认证能力", ResourceScopePlatform, "validate-session", "revoke-tenant-sessions", "issue-tenant-token"),
		resource("file.object", "文件对象", ResourceScopeTenant, "create", "read", "list", "download", "delete"),
		resource("tenant", "租户", ResourceScopePlatform, "create", "read", "list", "update", "delete", "grant", "assign-administrator"),
		resource("tenant.profile", "当前租户资料", ResourceScopeTenant, "read", "list", "update", "delete"),
		resource("tenant.selection", "当前用户租户选择", ResourceScopePrincipal, "list", "switch"),
		resource("tenant.context", "当前租户上下文", ResourceScopeTenant, "read"),
		resource("tenant.authorization", "租户授权", ResourceScopeTenant, "read", "grant", "assign-administrator"),
		resource("tenant.member", "租户成员", ResourceScopeTenant, "add", "read", "list", "update", "remove", "assign-role"),
		resource("tenant.department", "租户部门", ResourceScopeTenant, "create", "read", "list", "update", "delete", "assign-member"),
		resource("tenant.role", "租户角色", ResourceScopeTenant, "create", "read", "list", "update", "delete", "grant"),
		resource("permission.definition", "权限定义", ResourceScopePlatform, "create", "read", "list", "update", "delete"),
		resource("application", "应用", ResourceScopePlatform, "create", "read", "list", "update", "delete"),
		resource("tenant.application-grant", "租户应用授权", ResourceScopePlatform, "grant", "revoke", "read", "list"),
		resource("application.current", "当前租户应用", ResourceScopeTenant, "list", "read", "switch"),
		resource("navigation", "应用导航", ResourceScopePlatform, "create", "read", "list", "update", "delete"),
		resource("navigation.current", "当前租户应用导航", ResourceScopeTenant, "read"),
		resource("dictionary.definition", "数据字典", ResourceScopePlatform, "create", "read", "list", "update", "delete"),
		resource("dictionary.item", "数据字典项", ResourceScopePlatform, "create", "read", "list", "update", "delete"),
		resource("platform.config", "平台配置", ResourceScopePlatform, "create", "read", "list", "update", "delete"),
		resource("operation.log", "操作日志", ResourceScopeTenant, "create", "read", "list"),
		resource("security.log", "安全日志", ResourceScopeTenant, "read", "list"),
		resource("pbac.resource-action", "PBAC 资源动作注册表", ResourceScopePlatform, "list"),
		resource("pbac.global-policy", "全局策略", ResourceScopePlatform, "create", "read", "list", "create-version", "simulate", "publish", "set-status"),
		resource("pbac.tenant-policy", "租户策略", ResourceScopeTenant, "create", "read", "list", "create-version", "simulate", "publish", "set-status"),
		resource("data-permission.global-policy", "全局数据权限策略", ResourceScopePlatform, "create", "read", "list", "create-version", "simulate", "publish", "set-status"),
		resource("data-permission.tenant-policy", "租户数据权限策略", ResourceScopeTenant, "create", "read", "list", "create-version", "simulate", "publish", "set-status"),
	}
}

func resource(key, name string, scope ResourceScope, actions ...string) ResourceDefinition {
	definition := ResourceDefinition{Key: key, Name: name, Scope: scope, Actions: make([]ActionDefinition, 0, len(actions))}
	for _, action := range actions {
		definition.Actions = append(definition.Actions, ActionDefinition{Key: action, Name: action})
	}
	return definition
}
