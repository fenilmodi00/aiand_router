// Explicit Bake targets keep CI image builds independent of Compose profile
// discovery while retaining one cache scope per image.

target "server" {
  context    = "."
  dockerfile = "Dockerfile"
  # Pinned to the plain release image: the file's last stage is the
  # build.io variant (embedded emulator + boot migrations), which must
  # not become what CI bakes. Explicit target keeps the built image
  # byte-identical to before that stage was appended.
  target     = "build-release-stage"
  cache-from = [{ type = "gha", scope = "router-server" }]
  cache-to   = [{ type = "gha", scope = "router-server", mode = "max" }]
}

target "mitmproxy" {
  context    = "."
  dockerfile = "smoke/mitmproxy/Dockerfile"
  cache-from = [{ type = "gha", scope = "router-smoke-mitmproxy" }]
  cache-to   = [{ type = "gha", scope = "router-smoke-mitmproxy", mode = "max" }]
}

target "seed" {
  context    = "."
  dockerfile = "Dockerfile"
  target     = "seed-runtime"
  cache-from = [{ type = "gha", scope = "router-smoke-seed" }]
  cache-to   = [{ type = "gha", scope = "router-smoke-seed", mode = "max" }]
}
