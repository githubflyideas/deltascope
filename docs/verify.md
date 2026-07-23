# Release Impact Verification (verify)

Snapshots the whole machine before and after every release / upgrade /
config change, and automatically accounts for exactly what that change
touched. Answers the question every on-call engineer dreads: beyond what
I meant to change, did this release quietly touch anything else?

## Usage

Baseline before the release:

    deltascope verify start -name deploy-2024w30

Run your release (package upgrade / config change / service restart /
an ansible run ...).

Impact report after the release:

    deltascope verify report -name deploy-2024w30

## Wiring into CI / PRs

Generate Markdown to paste into a PR comment or Slack:

    deltascope verify report -name $CI_COMMIT_SHA -format md -title "Deploy impact" > impact.md

`report` exits with code 3 when changes are detected -- use that in CI to
require a manual sign-off:

    deltascope verify start -name $CI_COMMIT_SHA
    ./deploy.sh
    if ! deltascope verify report -name $CI_COMMIT_SHA -format md > impact.md; then
        gh pr comment --body-file impact.md   # changes found, post the report and wait for confirmation
    fi

## verify vs. statediff

- statediff is for routine watch: scheduled snapshots, seeing what
  naturally drifted between today and yesterday.
- verify is for a single release: an explicit baseline that precisely
  frames the impact of "this one operation", saved persistently by name.
