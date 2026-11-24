#!/bin/sh
set -eu

dir=${1:-.testdata}
mkdir -p "$dir"
dir=$(cd "$dir" && pwd)

fetch() {
	dest="$dir/$1"
	if [ "$(git -C "$dest" rev-parse HEAD 2>/dev/null || true)" = "$3" ]; then
		return
	fi
	echo "fetching $1 at $3" >&2
	rm -rf "$dest"
	git init -q "$dest"
	git -C "$dest" fetch -q --depth 1 "$2" "$3"
	git -C "$dest" -c advice.detachedHead=false checkout -q FETCH_HEAD
}

fetch doc-modular https://github.com/SUSE/doc-modular.git 698631336b029563bcba107d89975e0250bab4ac
fetch mkdocs-material https://github.com/squidfunk/mkdocs-material.git ec6319412936a1aa50285192fbc8b816d396c12c
fetch kind https://github.com/kubernetes-sigs/kind.git 3bf373491ba085b2fa266ae987349d6faad1df8a

docbook="$dir/doc-modular"
markdown="$dir/mkdocs-material/docs:$dir/kind/site/content"
echo "export SUSEDOC_TEST_DIR='$docbook'"
echo "export SUSEDOC_MD_TEST_DIRS='$markdown'"
if [ -n "${GITHUB_ENV:-}" ]; then
	echo "SUSEDOC_TEST_DIR=$docbook" >>"$GITHUB_ENV"
	echo "SUSEDOC_MD_TEST_DIRS=$markdown" >>"$GITHUB_ENV"
fi
