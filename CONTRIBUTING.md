# Contributing to remotekit

remotekit is Apache-2.0 and open to contributions. Its licence is permissive
in both directions, which is the point: it is the shared core beneath an
AGPL-3.0 project ([Chirp](https://github.com/barahn/chirp)) and a proprietary
one (Barahn), and neither could build on it otherwise.

## Contributor License Agreement

All contributors agree to this CLA before a pull request is merged. The terms
are identical across the three Fabrintek repositories, so a contributor agrees
once.

1. **Definitions**
   - *"You"* (or *"Contributor"*) means the copyright owner or legal entity
     authorized by the copyright owner submitting the Contribution.
   - *"Contribution"* means any source code, documentation, art assets, or
     other materials submitted via Git commit, pull request, or issue tracker
     to the project.
2. **Grant of Copyright License**: You hereby grant to the project maintainer
   (**Fabrintek Engenharia Digital Ltda**) a perpetual, worldwide,
   non-exclusive, no-charge, royalty-free, irrevocable copyright license to
   reproduce, prepare derivative works of, publicly display, publicly perform,
   sublicense, and distribute your Contributions and such derivative works.
3. **Grant of Patent License**: You hereby grant a perpetual, worldwide,
   non-exclusive, no-charge, royalty-free, irrevocable patent license to make,
   have made, use, offer to sell, sell, import, and otherwise transfer the
   Work, where such license applies only to those patent claims licensable by
   You that are necessarily infringed by Your Contribution(s).
4. **Originality & Authority**: You represent that each of Your Contributions
   is your original creation, and that you have the full legal right to grant
   the licenses set forth above. If your employer has rights to intellectual
   property that includes your Contributions, you represent that you have
   received permission to make Contributions on behalf of that employer.
5. **Open Source Commitment**: Contributions to remotekit are published under
   the **Apache License 2.0**. Fabrintek commits to keeping remotekit
   available under that licence or a compatible open-source successor.
6. **Dependency Direction**: Dependencies point inwards only. remotekit
   depends on neither of the others; Chirp may depend on remotekit; the Barahn
   platform may depend on both. A change that needs the arrow reversed is a
   design problem, not a licensing exception.

Clauses 2 and 3 largely restate what Apache-2.0 section 5 already provides for
inbound contributions. They are stated explicitly anyway, so that the same CLA
text can be used across all three repositories, where it is doing more work.

### Sign-off is required, and is not the CLA

Every commit must carry a `Signed-off-by` trailer:

```bash
git commit -s -m "fix(screen): release the shm segment on capture error"
```

That trailer is the [Developer Certificate of
Origin](https://developercertificate.org/): it certifies where the code came
from and that you have the right to submit it. It is not itself agreement to
the copyright and patent grants above. CI enforces the sign-off; the CLA is
agreed by opening a pull request against a repository that states these terms.

## Third-party code

remotekit distributes third-party code — notably the VP8 encoder in
`screen/codec/vp8`, a derivative of
[opd-ai/vp8](https://github.com/opd-ai/vp8) under the MIT Licence. That
attribution is recorded in [NOTICE](NOTICE) and the upstream text is retained
in `screen/codec/vp8/LICENSE.upstream`.

Apache-2.0 section 4(d) makes the NOTICE file binding on anyone redistributing
remotekit, so entries must not be removed. If you vendor further third-party
code, add its attribution to NOTICE in the same commit, and keep its licence
text next to the code.

Dependencies must be permissively licensed (MIT, BSD, ISC, Apache-2.0, Zlib).
A copyleft dependency would make remotekit unusable by one or both of the
projects it exists to serve, which defeats its purpose.

## Reporting security vulnerabilities

Do **not** open a public issue for a security-sensitive bug. Email
[security@mendsec.com](mailto:security@mendsec.com).
