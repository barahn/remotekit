## What changes

<!-- What and why. The how is in the diff. -->

## How it was verified

<!-- Commands run, cases tested. "CI passed" is not verification. -->

## Checklist

- [ ] Targets `develop` (only a release targets `main`).
- [ ] Every commit carries a DCO sign-off (`git commit -s`) — CI checks it.
- [ ] `go build ./... && go vet ./... && go test -race ./...` pass locally.
- [ ] No secret, token or credential in the diff, including tests and fixtures.
- [ ] No product opinion: no user, tenant, role, account or session policy. A
      consumer's policy belongs behind an extension point, in the consumer.
- [ ] No import of Chirp or of the Barahn platform, at any depth.
- [ ] New dependency: permissive licence (MIT, BSD, ISC, Apache-2.0, Zlib) — CI
      checks it. New vendored code: its attribution added to `NOTICE`.
- [ ] Security impact considered — input from a remote peer, file paths,
      injected input, anything reachable before consent. If there is any, it is
      described above.
