# Data-permission simulation

`POST /api/v1/data-permissions/global-policies/simulate` and
`POST /api/v1/data-permissions/tenant-policies/simulate` accept one candidate
data-permission document plus bounded, typed subject/resource attributes. They
return the current baseline, candidate result and whether the result changed.
The candidate is evaluated in an isolated snapshot: no policy row, published
version, Redis signal or runtime snapshot is changed.

Both routes require JWT and their respective data-permission management grant.
Tenant simulation requires candidate policy, subject and resource tenant IDs
to match the authenticated tenant. Attribute names are validated against the
owned Schema; generated SQL remains parameterized. SQL parameter values and
internal policy details are not returned. The existing endpoint unit tests and
Swagger consistency check cover route registration; runtime engine tests cover
candidate replacement, cancellation, scope checks and value redaction.
