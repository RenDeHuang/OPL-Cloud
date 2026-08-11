# Retired Cloud Agent Registry

Owner: `one-person-lab-cloud`
Purpose: `retired_agent_registry_model`
State: `history_tombstone`
Machine boundary: Retired-model provenance only. This is not an active schema,
registry, compatibility path, package source, or implementation instruction.

The former Cloud-owned `OPL Agent Registry` model is retired. It duplicated
package identity, version, and approval state across Fabric and Console and
could drift from the owning Package and its physical carrier. The Framework
package lock that existed when this tombstone was written is itself now a
`compatibility-to-delete` migration surface, not the replacement authority.

| Retired responsibility | Current owner |
| --- | --- |
| Package identity, kind, capabilities, requirements, and entrypoints | Package-owner descriptor and publication surface |
| Physical installed, enabled, callable, and carrier-current state | Fresh configured native-carrier readback |
| Install, update, remove, repair, enable, and disable | Configured native-carrier action delegated through the Framework `opl packages` surface |
| Lifecycle evidence | Package-owner publication refs plus native-carrier action/readback refs |
| Account/service availability | Console policy referencing an exact package ref |
| Compute, storage, environment, and connector requirements | Fabric binding derived from package requirements |
| User-visible Agent Instance | OPL App / Workspace |
| Portable service entrypoint | Package/domain owner contract |
| Service, Revision, Deployment, endpoint, and traffic state | OPL Serve |
| Invocation and Session lifecycle | OPL Runway; Serve projects status |
| Run and review evidence refs | OPL Ledger |

Current machine truth comes from package-owner descriptor/publication refs and
fresh configured native-carrier readback. Framework contracts, source, and
`opl packages ... --json` discover descriptors, delegate carrier actions, and
aggregate those owner/carrier projections without becoming another resolver,
lock, currentness authority, or lifecycle ledger. Cloud surfaces may project
the resulting refs but must not recreate the retired registry as an alias or
facade.
