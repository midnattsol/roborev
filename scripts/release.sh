#!/bin/bash
set -e

VERSION="$1"

if [ -z "$VERSION" ]; then
    echo "Usage: $0 <version>"
    echo "Example: $0 0.2.0"
    exit 1
fi

# Validate version format
if ! [[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "Error: Version must be in format X.Y.Z (e.g., 0.2.0)"
    exit 1
fi

TAG="v$VERSION"

# Check if tag already exists
if git rev-parse "$TAG" >/dev/null 2>&1; then
    echo "Error: Tag $TAG already exists"
    exit 1
fi

# Check for uncommitted changes
if ! git diff-index --quiet HEAD --; then
    echo "Error: You have uncommitted changes. Please commit or stash them first."
    exit 1
fi

# Find the previous tag
PREV_TAG=$(git describe --tags --abbrev=0 2>/dev/null || echo "")
if [ -z "$PREV_TAG" ]; then
    # No previous tag, use first commit
    RANGE="HEAD"
    echo "No previous release found. Generating changelog for all commits..."
else
    RANGE="$PREV_TAG..HEAD"
    echo "Generating changelog from $PREV_TAG to HEAD..."
fi

# Get commit log for changelog generation
COMMITS=$(git log $RANGE --pretty=format:"- %s (%h)" --no-merges)
DIFF_STAT=$(git diff --stat $PREV_TAG HEAD 2>/dev/null || git diff --stat $(git rev-list --max-parents=0 HEAD) HEAD)

# Create a temp file for the changelog
CHANGELOG_FILE=$(mktemp)
trap "rm -f $CHANGELOG_FILE" EXIT

# Use codex to generate the changelog
echo "Using codex to generate changelog..."
echo ""

codex -p "You are generating a changelog for roborev version $VERSION.

Here are the commits since the last release:
$COMMITS

Here's the diff summary:
$DIFF_STAT

Please generate a concise, user-focused changelog. Group changes into sections like:
- New Features
- Improvements
- Bug Fixes

Focus on user-visible changes. Skip internal refactoring unless it affects users.
Keep descriptions brief (one line each). Use present tense.
Output ONLY the changelog content, no preamble." > "$CHANGELOG_FILE"

echo ""
echo "=========================================="
echo "PROPOSED CHANGELOG FOR $TAG"
echo "=========================================="
cat "$CHANGELOG_FILE"
echo ""
echo "=========================================="
echo ""

# Ask for confirmation
read -p "Accept this changelog and create release $TAG? [y/N] " -n 1 -r
echo ""

if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    echo "Release cancelled."
    exit 0
fi

# Create the tag with changelog as message
echo "Creating tag $TAG..."
git tag -a "$TAG" -m "Release $VERSION

$(cat $CHANGELOG_FILE)"

echo "Pushing tag to origin..."
git push origin "$TAG"
git push origin HEAD

echo ""
echo "Release $TAG created and pushed successfully!"
echo ""
echo "GitHub release URL: https://github.com/wesm/roborev/releases/tag/$TAG"
