# Database-owned route policies

The database is the only runtime source of route authorization rules. HTTP and
gRPC code declares handlers and methods, but does not declare public paths or
permission requirements.

At startup each transport synchronizes its discovered routes into
`route_definitions`. IDs are UUIDv5 values derived from protocol, method, and
canonical route path. Removed routes become inactive; discovery never creates
or overwrites a policy.

An active business route must have one active row in
`route_policy_definitions`. Missing, disabled, invalid, or unresolvable policies
fail closed. Missing policies do not prevent the process from listening because
the first deployment must expose its synchronized route inventory for database
bootstrap; they never cause a request to be allowed. Permission nodes continue
to live in the existing `permissions`
table. `route_policy_permission_refs` connects a policy to those nodes and sets
the evaluation scope (`platform`, `tenant`, or `principal`).

## CEL environment

Policy expressions are CEL boolean expressions with these variables:

| Variable | Type | Meaning |
| --- | --- | --- |
| `anonymous` | bool | no verified JWT or PSK principal |
| `authenticated` | bool | a verified principal exists |
| `principal_type` | string | normalized principal type |
| `permissions` | map(string, bool) | authorization decisions for referenced permission keys |

Examples:

```cel
anonymous
```

```cel
authenticated && permissions["platform.user.page"]
```

```cel
authenticated && principal_type == "service_account"
```

Expressions are limited to 4096 bytes and eight permission references. Every
literal `permissions["..."]` reference must have exactly one active reference
row pointing to an active `permissions` record. Extra reference rows are
rejected to avoid unnecessary authorization calls.

## Cache consistency

All expressions are compiled while building a new immutable snapshot. A
request performs only a UUID lookup, authorization decisions for referenced
permissions, and evaluation of the precompiled CEL program. Successful refresh
atomically swaps the complete snapshot; a failed refresh leaves the last valid
snapshot in place.

The policy writer calls `routepolicy.Manager.Notify` after its database
transaction commits. Redis Pub/Sub provides immediate invalidation across
instances. `authorization.policy_refresh_interval` controls the database
revision poll that repairs missed Pub/Sub messages. The channel includes the
active environment and service name to prevent cross-environment invalidation.

Direct SQL maintenance cannot publish an invalidation event and therefore takes
effect on the next revision poll. Normal platform administration must use the
policy service/API so commit, local refresh, operation logging, and Redis
notification remain one controlled workflow.

On the first deployment, bootstrap only the policies for
`/api/v1/route-policies/get` and `/api/v1/route-policies/set` directly in the
database after route discovery. Assign them an existing platform-level
administration permission. Thereafter all route policy changes, including the
remaining initial policies, go through the protected API. There is no built-in
anonymous bootstrap route or code-level superuser bypass.
