# CI/CD Migration Plan: Adopting playwright-ci-go Patterns

## Overview

This plan migrates the blue-green-load-balancer CI/CD to match the playwright-ci-go pipeline exactly, with adaptations only where technically necessary.

## Key Principles

1. **No separation of integration tests** - All tests run together
2. **Exact test matrix pattern** - Follow playwright-ci-go's matrix structure
3. **Pre-built binaries** - Build in GHA, copy to distroless (no multi-stage builder)
4. **GHA cache optimization** - Separate cache scopes per architecture, then combine
5. **Shell over TypeScript** - Use `gh` CLI and curl for GitHub API
6. **Tool caching** - Cache goteststats, benchstat, golang-cover-diff by SHA

## Architecture Differences

| Aspect | playwright-ci-go | bluegreen (adapted) |
|--------|------------------|---------------------|
| Versioning | Playwright version-based | Date-based (YYYY.MM.patch) |
| Build step | Standard Go | Requires `templ generate` |
| Binaries | Single library | Two: bluegreen, bgctl |
| Docker base | Playwright browser image | distroless/static |
| Screenshot handling | Auto-fix browser screenshots | Not applicable |

---

## File Structure

```
.github/
├── workflows/
│   ├── ci.yml              # Reusable workflow (lint + test matrix)
│   ├── main.yml            # Main branch: tag, docker, pages
│   ├── pr.yml              # PR: CI + Claude fixes + auto-merge
│   └── index.html          # Benchmark visualization
├── dependabot.yml          # Go + Actions + Docker updates
Dockerfile                  # Copy pre-built binaries to distroless
```

---

## Phase 1: Reusable CI Workflow

### File: `.github/workflows/ci.yml`

```yaml
name: Continuous Integration

on: workflow_call

jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          persist-credentials: false

      - uses: actions/setup-go@v5
        with:
          go-version: stable

      - name: Install templ
        run: go install github.com/a-h/templ/cmd/templ@latest

      - name: Generate templates
        run: templ generate ./internal/ui/templates/

      - name: Run golangci-lint
        uses: golangci/golangci-lint-action@v6

  tests:
    defaults:
      run:
        shell: bash
    runs-on: ${{ matrix.runner }}
    strategy:
      fail-fast: false
      matrix:
        include:
          - runner: ubuntu-latest
            platform: amd64
          - runner: ubuntu-24.04-arm
            platform: arm64
    steps:
      - uses: actions/checkout@v4
        with:
          persist-credentials: true
          fetch-depth: 0
          token: ${{ secrets.GITHUB_TOKEN }}

      - name: Get go version
        id: go
        run: echo "version=$(grep '^go ' go.mod | cut -d ' ' -f 2)" >> "$GITHUB_OUTPUT"

      - uses: actions/setup-go@v5
        with:
          go-version: ${{ steps.go.outputs.version }}

      - name: Install templ
        run: go install github.com/a-h/templ/cmd/templ@latest

      - name: Generate templates
        run: templ generate ./internal/ui/templates/

      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v3

      - name: Figure out cache destination depending on runner
        id: cache
        run: |
          echo "cache=type=gha,mode=max,scope=${{ matrix.platform }}" >> "$GITHUB_OUTPUT"

      - name: Build binaries for this platform
        id: build-binaries
        run: |
          mkdir -p dist
          CGO_ENABLED=0 GOOS=linux GOARCH=${{ matrix.platform }} \
            go build -ldflags="-s -w" -o dist/bluegreen ./cmd/bluegreen
          CGO_ENABLED=0 GOOS=linux GOARCH=${{ matrix.platform }} \
            go build -ldflags="-s -w" -o dist/bgctl ./cmd/bgctl

      - name: Build Docker image locally with GHA cache
        uses: docker/build-push-action@v6
        with:
          context: .
          push: false
          file: Dockerfile
          cache-from: type=gha,scope=${{ matrix.platform }}
          cache-to: ${{ steps.cache.outputs.cache }}
          platforms: linux/${{ matrix.platform }}
          tags: ghcr.io/${{ github.repository }}:${{ github.sha }}
          load: true
          build-args: |
            TARGETARCH=${{ matrix.platform }}

      - name: Scan Image
        uses: anchore/scan-action@v5
        id: scan
        with:
          image: ghcr.io/${{ github.repository }}:${{ github.sha }}
          fail-build: false
          output-format: sarif

      - name: Upload Anchore Scan SARIF Report
        uses: github/codeql-action/upload-sarif@v3
        with:
          sarif_file: ${{ steps.scan.outputs.sarif }}

      - name: Install gotestsum
        uses: jaxxstorm/action-install-gh-release@v1
        with:
          repo: gotestyourself/gotestsum

      - name: Initialize CodeQL
        if: matrix.platform == 'amd64'
        uses: github/codeql-action/init@v3
        with:
          languages: go
          build-mode: manual

      - name: Build package for CodeQL
        run: go build ./...

      - name: Run tests (unit + integration)
        run: |
          mkdir -p html
          gotestsum --jsonfile tests.json --format standard-verbose \
            -- -race -tags=integration -covermode=atomic \
            -coverprofile="html/coverage.out" -cpuprofile="cpu.profile" \
            -timeout=15m ./...
        env:
          TESTCONTAINERS_RYUK_DISABLED: "true"

      - name: Perform CodeQL Analysis
        if: matrix.platform == 'amd64'
        uses: github/codeql-action/analyze@v3
        with:
          category: "/language:go"

      - name: Fetch goteststats @main SHA-1
        id: goteststats-main
        run: |
          sha1=$(curl \
            --header "Accept: application/vnd.github+json" \
            --silent \
              https://api.github.com/repos/getvictor/goteststats/branches/main | \
                jq --raw-output ".commit.sha")
          echo "sha1=$sha1" >>"$GITHUB_OUTPUT"

      - name: Cache goteststats
        id: cache-goteststats
        uses: actions/cache@v4
        with:
          key: goteststats-${{ matrix.platform }}-sha1-${{ steps.goteststats-main.outputs.sha1 }}
          path: ~/go/bin/goteststats

      - name: Install goteststats
        if: steps.cache-goteststats.outputs.cache-hit != 'true'
        run: go install github.com/getvictor/goteststats@latest

      - name: Fetch golang-cover-diff @main SHA-1
        id: golang-cover-diff-main
        run: |
          sha1=$(curl \
            --header "Accept: application/vnd.github+json" \
            --silent \
              https://api.github.com/repos/flipgroup/golang-cover-diff/branches/main | \
                jq --raw-output ".commit.sha")
          echo "sha1=$sha1" >>"$GITHUB_OUTPUT"

      - name: Cache golang-cover-diff
        id: cache-golang-cover-diff
        if: matrix.platform == 'amd64'
        uses: actions/cache@v4
        with:
          key: golang-cover-diff-${{ matrix.platform }}-sha1-${{ steps.golang-cover-diff-main.outputs.sha1 }}
          path: ~/go/bin/golang-cover-diff

      - name: Install golang-cover-diff
        if: matrix.platform == 'amd64' && steps.cache-golang-cover-diff.outputs.cache-hit != 'true'
        run: go install github.com/flipgroup/golang-cover-diff@main

      - name: Generate golang-cover-diff report
        if: matrix.platform == 'amd64' && github.event_name == 'pull_request'
        env:
          GITHUB_PULL_REQUEST_ID: ${{ github.event.number }}
          GITHUB_TOKEN: ${{ github.token }}
        run: |
          if curl --output /dev/null --silent --head --fail "https://mountain-reverie.github.io/blue-green-load-balancer/coverage.out"; then
            curl https://mountain-reverie.github.io/blue-green-load-balancer/coverage.out -o coverage-main.out
            golang-cover-diff coverage-main.out html/coverage.out
          fi

      - name: Generate goteststats report
        run: cat tests.json | goteststats

      - name: Fetch benchstat @master SHA-1
        id: benchstat-master
        run: |
          sha1=$(curl \
            --header "Accept: application/vnd.github+json" \
            --silent \
              https://api.github.com/repos/golang/perf/branches/master | \
                jq --raw-output ".commit.sha")
          echo "sha1=$sha1" >>"$GITHUB_OUTPUT"

      - name: Cache benchstat
        id: cache-benchstat
        uses: actions/cache@v4
        with:
          key: benchstat-${{ matrix.platform }}-sha1-${{ steps.benchstat-master.outputs.sha1 }}
          path: ~/go/bin/benchstat

      - name: Install benchstat
        if: steps.cache-benchstat.outputs.cache-hit != 'true'
        run: go install golang.org/x/perf/cmd/benchstat@latest

      - name: Run benchmark
        run: |
          go test -run='^$' -bench=. -count=7 -benchmem \
            -memprofile=html/mem.profile -cpuprofile=html/cpu.profile \
            ./... | tee html/benchmark.txt
          benchstat html/benchmark.txt

      - name: Download past benchmark result from GitHub Pages
        if: matrix.platform == 'amd64'
        run: |
          mkdir -p html/benchmark
          if curl --output /dev/null --silent --head --fail "https://mountain-reverie.github.io/blue-green-load-balancer/benchmark/benchmark-data.json"; then
            echo "Downloading past benchmark result"
            curl https://mountain-reverie.github.io/blue-green-load-balancer/benchmark/benchmark-data.json -o html/benchmark/benchmark-data.json
          else
            echo "No past benchmark result found"
            touch html/benchmark/benchmark-data.json
          fi
          cp .github/workflows/index.html html/benchmark/index.html
          curl "https://img.shields.io/badge/GO-Benchmark-green" > html/benchmark/badge.svg

      - name: Analyze benchmark result and generate pages
        if: matrix.platform == 'amd64'
        uses: benchmark-action/github-action-benchmark@v1
        with:
          tool: 'go'
          benchmark-data-dir-path: ./html/benchmark
          output-file-path: ./html/benchmark.txt
          external-data-json-path: ./html/benchmark/benchmark-data.json
          github-token: ${{ secrets.GITHUB_TOKEN }}
          fail-on-alert: true
          comment-on-alert: true
          auto-push: false

      - name: Generate data.js
        if: matrix.platform == 'amd64'
        run: |
          echo "window.BENCHMARK_DATA = $(cat html/benchmark/benchmark-data.json)" > html/benchmark/data.js

      - uses: actions/upload-artifact@v4
        with:
          name: "Tests analytics on ${{ matrix.platform }}"
          path: |
            tests.json
            html/coverage.out
            cpu.profile
            html/benchmark.txt
            dist/

      - name: Generate HTML coverage report
        if: matrix.platform == 'amd64'
        run: go tool cover -html=html/coverage.out -o html/coverage.html

      - name: Get coverage percentage
        if: matrix.platform == 'amd64'
        id: coverage
        run: |
          PERCENTAGE=$(go tool cover -func=html/coverage.out | grep total: | awk '{print $3}' | tr -d '%\n')
          echo "$PERCENTAGE% code coverage"
          echo "PERCENTAGE=$PERCENTAGE" >> $GITHUB_OUTPUT

      - name: Generate coverage badge
        if: matrix.platform == 'amd64'
        env:
          PERCENTAGE: ${{ steps.coverage.outputs.PERCENTAGE }}
        run: |
          COLOR="red"
          if (( $(echo "$PERCENTAGE >= 60" | bc -l) )); then
            COLOR="yellow"
          fi
          if (( $(echo "$PERCENTAGE >= 80" | bc -l) )); then
            COLOR="green"
          fi
          curl "https://img.shields.io/badge/Coverage-${PERCENTAGE}%25-${COLOR}" > html/coverage-badge.svg

      - name: Generate GitHub Pages artifact
        if: matrix.platform == 'amd64'
        uses: actions/upload-pages-artifact@v3
        with:
          path: html

      - name: Upload failed tests result
        if: failure()
        uses: actions/upload-artifact@v4
        with:
          name: "Failed tests on ${{ matrix.platform }}"
          path: |
            tests.json
            html/coverage.out
```

---

## Phase 2: Main Branch Workflow

### File: `.github/workflows/main.yml`

```yaml
name: Continuous Integration on main

on:
  push:
    branches:
      - main

concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: false

permissions:
  security-events: write
  packages: write
  attestations: write
  id-token: write
  repository-projects: write
  contents: write
  pages: write

jobs:
  ci:
    uses: ./.github/workflows/ci.yml

  tag:
    needs: ci
    runs-on: ubuntu-latest
    permissions:
      id-token: write
      repository-projects: write
      contents: write
    outputs:
      version: ${{ steps.semver.outputs.version }}
    steps:
      - name: Checkout
        uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - name: Get Next Version
        id: semver
        run: |
          # Date-based versioning: YYYY.MM.patch
          YEAR=$(date +%Y)
          MONTH=$(date +%-m)
          BASE="${YEAR}.${MONTH}"

          # Find highest patch for this year.month
          LATEST=$(git tag -l "${BASE}.*" | sort -t. -k3 -n | tail -1)
          if [ -z "$LATEST" ]; then
            PATCH=0
          else
            PATCH=$(echo "$LATEST" | cut -d. -f3)
            PATCH=$((PATCH + 1))
          fi

          VERSION="${BASE}.${PATCH}"
          echo "Version: $VERSION"
          echo "version=$VERSION" >> "$GITHUB_OUTPUT"

      - name: Create tag
        run: |
          git config --local user.name "GitHub Actions"
          git config --local user.email "github-actions[bot]@users.noreply.github.com"
          git tag -a "${{ steps.semver.outputs.version }}" -m "${{ steps.semver.outputs.version }} release"
          git push origin "${{ steps.semver.outputs.version }}"

  docker:
    permissions:
      packages: write
      attestations: write
      id-token: write
    needs: tag
    runs-on: ubuntu-latest
    steps:
      - name: Checkout
        uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - name: Setup Go
        uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      - name: Install templ
        run: go install github.com/a-h/templ/cmd/templ@latest

      - name: Generate templates
        run: templ generate ./internal/ui/templates/

      - name: Build binaries for amd64
        run: |
          mkdir -p dist/amd64
          CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
            go build -ldflags="-s -w" -o dist/amd64/bluegreen ./cmd/bluegreen
          CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
            go build -ldflags="-s -w" -o dist/amd64/bgctl ./cmd/bgctl

      - name: Build binaries for arm64
        run: |
          mkdir -p dist/arm64
          CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
            go build -ldflags="-s -w" -o dist/arm64/bluegreen ./cmd/bluegreen
          CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
            go build -ldflags="-s -w" -o dist/arm64/bgctl ./cmd/bgctl

      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v3

      - name: Log in to the Container registry
        uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Push image
        uses: docker/build-push-action@v6
        id: push
        with:
          context: .
          cache-from: |
            type=gha,scope=amd64
            type=gha,scope=arm64
          push: true
          tags: |
            ghcr.io/${{ github.repository }}:${{ needs.tag.outputs.version }}
            ghcr.io/${{ github.repository }}:latest
          platforms: linux/arm64,linux/amd64

      - name: Generate artifact attestation
        uses: actions/attest-build-provenance@v2
        with:
          subject-name: ghcr.io/${{ github.repository }}
          subject-digest: ${{ steps.push.outputs.digest }}
          push-to-registry: true

  deploy-gh-pages:
    concurrency:
      group: 'pages'
      cancel-in-progress: true
    needs: ci
    environment:
      name: github-pages
      url: ${{ steps.deployment.outputs.page_url }}
    permissions:
      pages: write
      id-token: write
    runs-on: ubuntu-latest
    steps:
      - name: Upload to GitHub Pages
        id: deployment
        uses: actions/deploy-pages@v4

  trigger-dependabot:
    needs: ci
    runs-on: ubuntu-latest
    permissions:
      pull-requests: write
    steps:
      - name: Find first open Dependabot PR
        id: find-pr
        run: |
          PR_NUMBER=$(gh pr list --author app/dependabot --state open --json number --jq '.[0].number // empty' --limit 1)
          if [ "$PR_NUMBER" != "null" ] && [ -n "$PR_NUMBER" ]; then
            echo "pr_number=$PR_NUMBER" >> "$GITHUB_OUTPUT"
            echo "Found Dependabot PR #$PR_NUMBER"
          else
            echo "No open Dependabot PRs found"
          fi
        env:
          GH_TOKEN: ${{ secrets.DEPENDABOT_RECREATE_PAT }}
          GH_REPO: ${{ github.repository }}

      - name: Comment to trigger recreate
        if: steps.find-pr.outputs.pr_number != ''
        run: |
          gh pr comment ${{ steps.find-pr.outputs.pr_number }} --body "@dependabot recreate"
        env:
          GH_TOKEN: ${{ secrets.DEPENDABOT_RECREATE_PAT }}
          GH_REPO: ${{ github.repository }}
```

---

## Phase 3: Pull Request Workflow

### File: `.github/workflows/pr.yml`

```yaml
name: Continuous Integration

on:
  pull_request:
  workflow_dispatch:

permissions:
  security-events: write
  packages: read
  actions: read
  pull-requests: write
  contents: write
  id-token: write

jobs:
  ci:
    uses: ./.github/workflows/ci.yml

  claude-fix-dependabot-failures:
    runs-on: ubuntu-latest
    needs: ci
    if: always() && needs.ci.result == 'failure' && github.event.pull_request.user.login == 'dependabot[bot]'
    concurrency:
      group: claude-fix-dependabot-failures-${{ github.event.pull_request.number }}
      cancel-in-progress: false
    steps:
      - name: Checkout
        uses: actions/checkout@v4
        with:
          persist-credentials: true
          fetch-depth: 0
          token: ${{ secrets.DEPENDABOT_PAT }}

      - name: Check for previous Claude attempts
        id: check-previous-attempts
        run: |
          LAST_COMMIT_MSG=$(git log -1 --pretty=format:'%s')
          LAST_COMMIT_AUTHOR=$(git log -1 --pretty=format:'%ae')

          echo "Last commit message: $LAST_COMMIT_MSG"
          echo "Last commit author: $LAST_COMMIT_AUTHOR"

          if [[ "$LAST_COMMIT_MSG" == *"Generated with [Claude Code]"* ]] || [[ "$LAST_COMMIT_AUTHOR" == "noreply@anthropic.com" ]]; then
            echo "Previous Claude attempt detected. Skipping to avoid infinite loop."
            echo "should_run=false" >> "$GITHUB_OUTPUT"
          else
            echo "No previous Claude attempt found. Proceeding with fix."
            echo "should_run=true" >> "$GITHUB_OUTPUT"
          fi

      - name: Dependabot metadata
        if: steps.check-previous-attempts.outputs.should_run == 'true'
        id: dependabot-metadata
        uses: dependabot/fetch-metadata@v2
        with:
          github-token: "${{ secrets.GITHUB_TOKEN }}"

      - name: Check if Go ecosystem
        if: steps.check-previous-attempts.outputs.should_run == 'true'
        id: check-go-ecosystem
        run: |
          if [[ "${{ steps.dependabot-metadata.outputs.package-ecosystem }}" == "go_modules" ]]; then
            echo "This is a Go module update. Proceeding with Claude fix."
            echo "is_go_ecosystem=true" >> "$GITHUB_OUTPUT"
          else
            echo "This is not a Go module update. Skipping Claude fix."
            echo "is_go_ecosystem=false" >> "$GITHUB_OUTPUT"
          fi

      - name: Setup Go
        if: steps.check-previous-attempts.outputs.should_run == 'true' && steps.check-go-ecosystem.outputs.is_go_ecosystem == 'true'
        uses: actions/setup-go@v5
        with:
          go-version: stable

      - name: Install tools
        if: steps.check-previous-attempts.outputs.should_run == 'true' && steps.check-go-ecosystem.outputs.is_go_ecosystem == 'true'
        run: |
          go install github.com/a-h/templ/cmd/templ@latest
          go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
          go install golang.org/x/tools/gopls@latest
          echo "$HOME/go/bin" >> $GITHUB_PATH

      - name: Run Claude Code Action to fix build/test failures
        if: steps.check-previous-attempts.outputs.should_run == 'true' && steps.check-go-ecosystem.outputs.is_go_ecosystem == 'true'
        id: claude-fix
        uses: anthropics/claude-code-action@beta
        with:
          claude_code_oauth_token: ${{ secrets.CLAUDE_CODE_OAUTH_TOKEN }}
          claude_args: |
            --allowedTools 'Edit,MultiEdit,Write,Read,Glob,Grep,LS,Bash(git:*),Bash(go:*),Bash(templ:*),Bash(golangci-lint:*),Bash(gh:*)'
            --mcp-config '{"mcpServers": {"gopls": {"command": "gopls", "args": ["mcp"] }}}'
          prompt: |
            /fix-ci

            This is a dependabot PR that has failed CI. Please analyze and fix the build or test failures systematically.

            **Context:**
            - Dependabot PR updating: ${{ steps.dependabot-metadata.outputs.dependency-names }}
            - Update type: ${{ steps.dependabot-metadata.outputs.update-type }}
            - Package ecosystem: ${{ steps.dependabot-metadata.outputs.package-ecosystem }}
            - Previous version: ${{ steps.dependabot-metadata.outputs.previous-version }}
            - New version: ${{ steps.dependabot-metadata.outputs.new-version }}

            **Required Analysis Steps:**
            1. **Generate templates first**: Run `templ generate ./internal/ui/templates/`
            2. **Initial Assessment**: Run `go build ./...`, `golangci-lint run` and `go test -v ./...` to identify specific failures
            3. **Root Cause Analysis**: Determine what API changes or breaking changes caused the failures
            4. **Dependency Check**: Run `go mod tidy` and check if additional dependencies are needed
            5. **Fix Implementation**: Make targeted fixes for compilation/test failures
            6. **Validation**: Re-run `go build ./...` and `go test -v ./...` to confirm fixes work
            7. **Final Status**: Provide clear summary of what was fixed

            **Critical Requirements:**
            - **Document your progress**: After each major step, clearly state what you found and what you're doing
            - **Be specific**: When fixing issues, explain what changed in the dependency and how you're adapting to it
            - **Minimal changes only**: Only modify code that's broken by the dependency update
            - **Preserve behavior**: Ensure existing functionality remains unchanged
            - **Test thoroughly**: All tests must pass before completing
            - **Do not commit anything**: Next steps will handle committing if successful

            **Output Format for Final Status:**
            ## Fix Summary
            - **Build Status**: [PASSED/FAILED]
            - **Test Status**: [PASSED/FAILED]
            - **Files Modified**: [List of files you changed]
            - **Key Changes**: [Brief description of main fixes applied]
            - **Dependency Impact**: [What changed in the updated dependencies]

      - name: Detect if any changes were made by Claude
        if: steps.check-previous-attempts.outputs.should_run == 'true' && steps.check-go-ecosystem.outputs.is_go_ecosystem == 'true' && steps.claude-fix.outcome == 'success'
        id: detect-claude-changes
        run: |
          if [ -n "$(git status --porcelain)" ]; then
            echo "Changes detected after Claude fix."
            echo "changes_made=true" >> "$GITHUB_OUTPUT"
          else
            echo "No changes detected after Claude fix."
            echo "changes_made=false" >> "$GITHUB_OUTPUT"
          fi

      - name: "Import GPG key for Claude fixes"
        if: steps.check-previous-attempts.outputs.should_run == 'true' && steps.check-go-ecosystem.outputs.is_go_ecosystem == 'true' && steps.claude-fix.outcome == 'success' && steps.detect-claude-changes.outputs.changes_made == 'true'
        id: import-gpg-claude
        uses: crazy-max/ghaction-import-gpg@v6
        with:
          gpg_private_key: ${{ secrets.GPG_KEY_PRIVATE }}
          passphrase: ${{ secrets.GPG_KEY_PASSWORD }}
          git_user_signingkey: true
          git_commit_gpgsign: true

      - name: "Commit and push Claude fixes"
        id: commit-claude-fixes
        if: steps.check-previous-attempts.outputs.should_run == 'true' && steps.check-go-ecosystem.outputs.is_go_ecosystem == 'true' && steps.claude-fix.outcome == 'success' && steps.detect-claude-changes.outputs.changes_made == 'true'
        env:
          TOKEN: ${{ secrets.DEPENDABOT_PAT }}
        run: |
          git add -A
          git commit -S -m "fix: automated dependabot fixes by Claude Code Action

             - Fixed build and test failures caused by dependency updates
             - Updated packages: ${{ steps.dependabot-metadata.outputs.dependency-names }}
             - Update type: ${{ steps.dependabot-metadata.outputs.update-type }}

             🤖 Generated with [Claude Code](https://claude.ai/code)

             Co-Authored-By: Claude <noreply@anthropic.com>"
          if [ $? -eq 0 ]; then
            echo "Changes detected and committed."
            echo "changes_detected=true" >> "$GITHUB_OUTPUT"
            COMMIT_HASH=$(git rev-parse HEAD)
            echo "commit_hash=$COMMIT_HASH" >> "$GITHUB_OUTPUT"
            git remote set-url --push origin https://x-access-token:${TOKEN}@github.com/${GITHUB_REPOSITORY}.git
            git push origin HEAD:${{ github.head_ref }}
          else
            echo "No changes to commit."
            echo "changes_detected=false" >> "$GITHUB_OUTPUT"
          fi

      - name: Create summary comment on PR
        if: steps.check-previous-attempts.outputs.should_run == 'true' && steps.check-go-ecosystem.outputs.is_go_ecosystem == 'true' && always()
        uses: peter-evans/create-or-update-comment@v4
        with:
          issue-number: ${{ github.event.pull_request.number }}
          body: |
            ## 🤖 Claude Code Automated Fix Attempt

            Claude Code Action has attempted to automatically fix the CI failures in this dependabot PR.

            **Dependabot Update Details:**
            - **Updated packages:** ${{ steps.dependabot-metadata.outputs.dependency-names }}
            - **Update type:** ${{ steps.dependabot-metadata.outputs.update-type }}
            - **Package ecosystem:** ${{ steps.dependabot-metadata.outputs.package-ecosystem }}

            **Claude Action Status:** ${{ steps.claude-fix.outcome }}

            ${{ steps.claude-fix.outcome == 'success' && steps.commit-claude-fixes.outputs.changes_detected == 'true' && '✅ Claude has successfully fixed the build/test failures and committed the changes.' || steps.claude-fix.outcome == 'success' && '✅ Claude analyzed the issues but no changes were needed.' || '❌ Claude encountered issues. Manual intervention may be required.' }}

            ${{ steps.commit-claude-fixes.outputs.commit_hash && format('**Commit:** https://github.com/{0}/commit/{1}', github.repository, steps.commit-claude-fixes.outputs.commit_hash) || '' }}
          token: ${{ secrets.GITHUB_TOKEN }}

  dependabot:
    runs-on: ubuntu-latest
    if: github.event.pull_request.user.login == 'dependabot[bot]'
    steps:
      - name: Dependabot metadata
        id: dependabot-metadata
        uses: dependabot/fetch-metadata@v2

      - name: Enable auto-merge for Dependabot PRs
        if: steps.dependabot-metadata.outputs.maintainer-changes && (steps.dependabot-metadata.outputs.package-ecosystem == 'go_modules' || steps.dependabot-metadata.outputs.package-ecosystem == 'github_actions')
        run: gh pr merge --auto --merge "${{ github.event.pull_request.html_url }}"
        env:
          GH_TOKEN: ${{ secrets.DEPENDABOT_PAT }}
```

---

## Phase 4: Dockerfile (Distroless)

### File: `Dockerfile`

```dockerfile
# Dockerfile for blue-green-load-balancer
# Binaries are pre-built in GitHub Actions and copied here
# This avoids multi-stage builds and maximizes GHA cache efficiency

ARG TARGETARCH=amd64

FROM gcr.io/distroless/static-debian12:nonroot

ARG TARGETARCH

# Copy pre-built binaries from dist directory
# These are built per-architecture in the CI workflow
COPY dist/${TARGETARCH}/bluegreen /usr/local/bin/bluegreen
COPY dist/${TARGETARCH}/bgctl /usr/local/bin/bgctl

# Copy static files if any
COPY static/ /app/static/

# Run as non-root user (distroless provides this)
USER nonroot:nonroot

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/bluegreen"]
CMD ["-config", "/etc/bluegreen/config.yaml"]
```

---

## Phase 5: Dependabot Configuration

### File: `.github/dependabot.yml`

```yaml
version: 2
updates:
  - package-ecosystem: "gomod"
    directory: "/"
    schedule:
      interval: "weekly"
  - package-ecosystem: "github-actions"
    directory: "/"
    schedule:
      interval: "weekly"
  - package-ecosystem: "docker"
    directory: "/"
    schedule:
      interval: "weekly"
```

---

## Phase 6: Benchmark Visualization

### File: `.github/workflows/index.html`

Copy exactly from playwright-ci-go (the full 299-line HTML file with Chart.js visualization).

---

## Required Secrets

| Secret | Purpose | How to Create |
|--------|---------|---------------|
| `DEPENDABOT_PAT` | Auto-merge PRs, push commits | GitHub PAT with `repo` scope |
| `DEPENDABOT_RECREATE_PAT` | Trigger recreations | GitHub PAT with `repo` scope |
| `CLAUDE_CODE_OAUTH_TOKEN` | AI-assisted fixes | Claude Code OAuth token |
| `GPG_KEY_PRIVATE` | Signed commits | GPG private key (armored) |
| `GPG_KEY_PASSWORD` | GPG passphrase | Passphrase for GPG key |

---

## GHA Cache Strategy Explanation

The multi-architecture Docker build has a limitation: GHA cache doesn't work well when building multiple platforms in one action. The solution:

1. **CI workflow (per-platform)**: Each matrix job builds for ONE platform with `cache-to: type=gha,scope=${{ matrix.platform }}`
2. **Main workflow (multi-platform)**: The docker job pulls from BOTH caches: `cache-from: type=gha,scope=amd64` AND `type=gha,scope=arm64`
3. This maximizes cache hits because:
   - amd64 layers are cached from amd64 CI run
   - arm64 layers are cached from arm64 CI run
   - Multi-platform build uses both caches

---

## Implementation Order

| Step | Files | Description |
|------|-------|-------------|
| 1 | `Dockerfile` | Create distroless Dockerfile |
| 2 | `.github/dependabot.yml` | Enable dependency updates |
| 3 | `.github/workflows/index.html` | Copy benchmark visualization |
| 4 | `.github/workflows/ci.yml` | Create reusable CI workflow |
| 5 | `.github/workflows/main.yml` | Create main branch workflow |
| 6 | `.github/workflows/pr.yml` | Create PR workflow |
| 7 | Configure secrets | Set up all required secrets |
| 8 | Enable GitHub Pages | Settings → Pages → GitHub Actions |
| 9 | Delete `.github/workflows/ci.yaml` | Remove old workflow |
| 10 | Test with a PR | Verify full pipeline |

---

## Key Optimizations Preserved from playwright-ci-go

1. **Tool caching by SHA**: goteststats, golang-cover-diff, benchstat cached by upstream commit SHA
2. **Conditional execution**: CodeQL, coverage diff, pages only on amd64
3. **fail-fast: false**: All matrix jobs complete even if one fails
4. **Concurrency groups**: Prevent parallel conflicting runs
5. **Artifact uploads**: Failed tests, coverage, benchmarks all preserved
6. **Security scanning**: Anchore + CodeQL on every build
7. **GPG signing**: All automated commits are signed
8. **Infinite loop prevention**: Check for previous Claude attempts

Sources:
- [GoogleContainerTools/distroless](https://github.com/GoogleContainerTools/distroless)
- [Distroless Container Images Tutorial](https://labs.iximiuz.com/tutorials/gcr-distroless-container-images)
- [Alpine, Distroless or Scratch](https://medium.com/google-cloud/alpine-distroless-or-scratch-caac35250e0b)
