package version

// String is the single in-repo agentbus version surface. It is a package-level
// variable (not a constant) so the released version can be injected at link time
// via `-X github.com/tk-425/agentbus/internal/version.String=<tag>`. The literal
// below is only the development fallback for uninjected builds.
var String = "v0.4.5"
