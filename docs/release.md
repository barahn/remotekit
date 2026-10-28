# Releasing remotekit

remotekit is a Go module that others build on, Chirp first. A release is an
annotated tag on `main`, the trunk every pull request lands on
(`docs/branching.md`), and a GitHub Release with its notes. Every repository
of the Barahn organisation versions the same way; Chirp's `docs/release.md`
says the same for an application.

## Versions

- **Semantic Versioning, `vMAJOR.MINOR.PATCH`**, always with the `v` Go
  requires: MAJOR for a breaking change to the exported API, MINOR for
  something new that breaks nothing, PATCH for a fix that changes no API.
  Before 1.0 a breaking change may go in a MINOR, and is still named in the
  notes.
- **Pre-releases** are `vX.Y.Z-rc.N` (or `-alpha.N`, `-beta.N`). Go orders
  them before `vX.Y.Z`, so `go get` picks them only when asked by name.
- **Tags are annotated and never move or go away.** A published version is
  in the Go module proxy and checksum database for good: a tag moved
  afterwards breaks every build that fetched the old one. A wrong release is
  answered with a new version, and with a `retract` in `go.mod` if it must
  not be used. Deleting a tag needs the maintainer's explicit confirmation
  and a note of what it breaks.
- **v2 and later** change the module path to `github.com/barahn/remotekit/vN`,
  and every internal import with it, in the same release.
- **`signal/nostr` is a module of its own**, tagged `signal/nostr/vX.Y.Z`. It
  is released when it changes, and requires a released remotekit, never a
  pseudo-version.

## When to release

**A release validates a set of changes, not a pull request.** Merging a pull
request into `main` does not make a version. remotekit is released when a
Chirp milestone that needs it is ready:

1. Chirp's branch pins the remotekit commit on `main` it needs (a
   pseudo-version) while the work goes on.
2. For Chirp's release candidate, remotekit gets `vX.Y.Z-rc.N` at that
   commit, and Chirp's candidate pins it.
3. When Chirp's candidate has passed its end-to-end checks (Chirp's
   `docs/release.md`), remotekit gets `vX.Y.Z` at the same commit, and Chirp
   pins that for its final release.

A fix another consumer needs urgently can go out as a PATCH on its own,
through the same steps.

## Making one

1. **Propose it.** From the commits since the last tag (`feat`, `fix`, and
   `feat!`/`BREAKING CHANGE` for breaking ones; `docs`, `chore`, `refactor`,
   `test` and `ci` alone change no version unless they touch the API),
   write down:
   - the proposed version and why it is a MAJOR, MINOR or PATCH;
   - the changes it carries;
   - their impact: removed, renamed or changed APIs, new dependencies,
     behaviour that tightens, and how to migrate, with before and after code
     for anything breaking;
   - the exact commands.

   Then ask the maintainer: *"Posso criar e publicar a tag vX.Y.Z?"* Nothing
   is tagged without that yes.
2. **Check `main` at the commit to tag:** CI green there, and locally clean:

   ```sh
   go mod tidy && git diff --exit-code go.mod go.sum
   go build ./... && go vet ./... && go test ./...
   ```

3. **Tag and push:**

   ```sh
   git tag -a vX.Y.Z <commit> -m "remotekit vX.Y.Z" -m "<summary>"
   git push origin vX.Y.Z
   gh release create vX.Y.Z --verify-tag --notes-file notes.md   # add --prerelease for an rc
   ```

4. **Check the module proxy has it:**

   ```sh
   GOPROXY=https://proxy.golang.org go list -m -versions github.com/barahn/remotekit
   ```

The release notes are the changelog: new, fixed, security, dependencies,
and what upgrading needs. They follow the shape of v0.6.1's and v0.7.0's.

## Road to 1.0

remotekit reaches 1.0 together with Chirp, not before: 1.0 promises an API
that does not break, and Chirp is where the API is proven. On top of
Chirp's criteria, remotekit needs:

- every exported API used by Chirp, or documented as experimental;
- no flaky test in CI (#135);
- screen capture and input on every platform Chirp 1.0 supports
  (macOS: #72; Wayland: #78).

`SPDX-License-Identifier: Apache-2.0`
Copyright (C) 2026 Fabrintek Engenharia Digital Ltda
