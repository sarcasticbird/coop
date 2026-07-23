# Third-Party Notices

The embedded image definition installs third-party software from a pinned Flox
base image, Coop's locked core Flox environment, and a pinned Nixpkgs revision
for configured tools. Those packages remain subject to their own licenses,
notices, and terms; coop's Apache-2.0 license does not replace them.

The public beta builds this image locally and does not publish or redistribute
the resulting image. Before redistributing a locally built image, review the
licenses and terms for its complete package closure.
Repository and global `[tools].packages` additions expand that closure and must
be reviewed as well. Trusted `[[tools.github_release]]` declarations also copy
third-party executables into the local image. Their upstream licenses, notices,
and redistribution terms apply even though Coop verifies their release-asset
digests.

In particular, the image definition installs Claude Code, whose published
license states that use is subject to Anthropic's terms and does not provide an
express general redistribution grant. Do not redistribute that binary as part
of a coop image without confirming permission. Codex is published under
Apache-2.0 and OpenCode under MIT; their license and notice obligations still
apply to redistributed copies.
