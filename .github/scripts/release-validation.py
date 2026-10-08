#!/usr/bin/env python3
"""Fail-closed checks for the manually dispatched experimental release workflow.

This helper only reads GitHub state and local files. Publication is kept in the
workflow's two contents:write jobs; tests replace the read-only API function.
"""
import argparse
import base64
import hashlib
import importlib.util
import json
import os
import pathlib
import re
import sys
import time
import urllib.error
import urllib.request

_FULL_SPEC = importlib.util.spec_from_file_location("ci_full_validation", pathlib.Path(__file__).with_name("ci_full_validation.py"))
full_validation = importlib.util.module_from_spec(_FULL_SPEC)
_FULL_SPEC.loader.exec_module(full_validation)

REPOSITORY = "webkaz-labs/sobalink"
PROJECT = "github.com/" + REPOSITORY
WORKFLOW = ".github/workflows/prerelease.yml"
IDENTITY = "https://github.com/" + REPOSITORY + "/" + WORKFLOW + "@refs/heads/main"
ISSUER = "https://token.actions.githubusercontent.com"
TARGETS = ("linux-amd64", "linux-arm64", "darwin-arm64", "windows-amd64")
VERSION = re.compile(r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*\Z")


def require(condition, message):
    if not condition:
        raise ValueError(message)


def validate_inputs(version, commit):
    require(bool(VERSION.fullmatch(version)), "version must be X.Y.Z-prerelease, without v or build metadata")
    for identifier in version.split("-", 1)[1].split("."):
        require(not (identifier.isdigit() and len(identifier) > 1 and identifier[0] == "0"), "numeric prerelease identifiers cannot start with zero")
    require(bool(re.fullmatch(r"[0-9a-f]{40}", commit)), "tested_commit must be a full lowercase SHA")


def api(path, missing=False):
    request = urllib.request.Request("https://api.github.com/repos/" + REPOSITORY + ("/" + path if path else ""),
                                     headers={"Authorization": "Bearer " + os.environ["GH_TOKEN"],
                                              "Accept": "application/vnd.github+json",
                                              "X-GitHub-Api-Version": "2022-11-28"})
    try:
        with urllib.request.urlopen(request, timeout=60) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        if missing and error.code == 404:
            return None
        raise


def source_gate(version, commit, env):
    validate_inputs(version, commit)
    require(env.get("GITHUB_REPOSITORY") == REPOSITORY, "unexpected repository")
    require(env.get("GITHUB_EVENT_NAME") == "workflow_dispatch", "manual dispatch required")
    require(env.get("GITHUB_REF") == "refs/heads/main", "dispatch must use main")
    require(env.get("GITHUB_SHA") == commit, "tested_commit must match this workflow's exact source SHA")
    require(env.get("GITHUB_WORKFLOW_REF") == REPOSITORY + "/" + WORKFLOW + "@refs/heads/main", "unexpected workflow identity")
    repository = api("")
    require(repository["full_name"] == REPOSITORY and repository["private"] is False and repository["default_branch"] == "main", "expected public repository with main default branch")
    require(api("git/ref/heads/main")["object"]["sha"] == commit, "main has moved; test and dispatch the new commit")
    latest = full_validation.select_candidate(api, commit)
    proof = full_validation.audit(api, commit, latest=latest, local_root=pathlib.Path(__file__).resolve().parents[2])
    print("Exact-source full-CI gate:", latest["html_url"])
    return dict(latest, full_validation_proof=proof)




def find_release(tag):
    # The by-tag endpoint returns published releases only. Authenticated listing
    # also includes drafts visible to the token; fetch their stable numeric ID.
    # Never infer that a draft is absent from a by-tag 404 alone.
    release = api("releases/tags/" + tag, missing=True)
    if release is not None:
        return release
    matches = []
    for page in range(1, 101):
        releases = api("releases?per_page=100&page=" + str(page))
        require(isinstance(releases, list), "invalid release listing")
        matches.extend(item for item in releases if item["tag_name"] == tag)
        require(len(matches) <= 1, "multiple releases use the requested tag")
        if len(releases) < 100:
            if not matches:
                return None
            release_id = matches[0]["id"]
            require(type(release_id) is int and release_id > 0, "invalid release ID")
            release = api("releases/" + str(release_id))
            require(release["id"] == release_id and release["tag_name"] == tag, "release changed during lookup")
            return release
    raise ValueError("release listing exceeded the review limit; refusing an incomplete lookup")

def release_state(version, commit, required=False, published=False):
    validate_inputs(version, commit)
    tag = "v" + version
    ref = api("git/ref/tags/" + tag, missing=True)
    if ref is not None:
        require(ref["object"]["type"] == "commit" and ref["object"]["sha"] == commit, "existing tag must point directly to tested_commit; tags are never moved")
    release = find_release(tag)
    require(not required or release is not None, "expected release does not exist")
    if release is not None:
        require(ref is not None, "release tag is missing")
        # GitHub may retain main when target_commitish is ignored for an existing
        # tag. The exact lightweight tag SHA above is the authoritative source.
        require(release["tag_name"] == tag and release["target_commitish"] in (commit, "main"), "release source differs from tested_commit")
        require(release["prerelease"] is True, "only prereleases may be created or resumed")
        require(release["draft"] is (not published), "published releases are immutable; choose a new version")
        require("<!-- sobalink-source:" + commit + " -->" in release["body"], "release lacks the exact source marker")
    if release is not None:
        require(len(release["assets"]) == len({a["name"] for a in release["assets"]}), "duplicate remote assets")
        require({a["name"] for a in release["assets"]} <= filenames(version) | {"packslip.sigstore.json"}, "unexpected remote assets; manual review required")
    return ref, release


def filenames(version):
    result = {"packslip.toml", "SHA256SUMS"}
    for target in TARGETS:
        stem = "sobalink-" + version + "-" + target
        result.update(stem + suffix for suffix in ((".zip" if target.startswith("windows-") else ".tar.gz"), ".cdx.json", ".build.json", ".notices.json"))
    return result


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def check_assets(root, version, commit, bundle=False):
    validate_inputs(version, commit)
    expected = filenames(version)
    if bundle:
        expected.add("packslip.sigstore.json")
    require({p.name for p in root.iterdir()} == expected, "release asset set differs from the exact four-target inventory")
    require(all(p.is_file() and not p.is_symlink() for p in root.iterdir()), "release assets must be regular files")
    checksums = {}
    for line in (root / "SHA256SUMS").read_text(encoding="utf-8").splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  ([A-Za-z0-9_.-]+)", line)
        require(match is not None, "invalid checksum line")
        digest, name = match.groups()
        require(name not in checksums, "duplicate checksum entry")
        checksums[name] = digest
    require(set(checksums) == filenames(version) - {"SHA256SUMS"}, "incomplete checksums")
    for name, digest in checksums.items():
        require(sha256(root / name) == digest, "checksum mismatch: " + name)
    for target in TARGETS:
        stem = "sobalink-" + version + "-" + target
        meta = json.loads((root / (stem + ".build.json")).read_text(encoding="utf-8"))
        require(meta["product"] == "sobalink" and meta["project"] == PROJECT and meta["version"] == version and meta["source_commit"] == commit and meta["target"] == target, "incorrect build identity: " + target)
        require(meta["build_tags"] == "ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy", "unexpected network build tags")
        require(meta["go_version"] == "go1.27.1" and meta["cgo_enabled"] is False and meta["trimpath"] is True and meta["buildvcs"] is False, "unexpected build settings")
        require(meta["frontend"]["node_version"] == "24.19.0" and meta["frontend"]["npm_version"] == "11.9.0", "unexpected frontend toolchain")
        require(re.fullmatch(r"[0-9a-f]{64}", meta["frontend"]["lock_sha256"]) is not None, "missing frontend lock digest")
        require(bool(meta["frontend"]["assets"]), "missing embedded frontend inventory")
        bom = json.loads((root / (stem + ".cdx.json")).read_text(encoding="utf-8"))
        require(bom["bomFormat"] == "CycloneDX" and bom["specVersion"] == "1.5", "invalid SBOM")
        notices = json.loads((root / (stem + ".notices.json")).read_text(encoding="utf-8"))
        require(bool(notices["modules"]) and bool(notices["frontend_modules"]), "empty dependency notices")
        for module in notices["modules"] + notices["frontend_modules"] + [notices["go_standard_library"]]:
            require(bool(module["notices"]), "missing module license notices")
    print("Verified four native targets, source identity, SBOMs, notices, and all distribution checksums")


def check_bundle(root, version, commit):
    """Semantic/digest checks only; call packslip verify first for cryptography."""
    validate_inputs(version, commit)
    raw = json.loads((root / "packslip.sigstore.json").read_text(encoding="utf-8"))
    statement = json.loads(base64.b64decode(raw["dsseEnvelope"]["payload"], validate=True))
    require(statement["_type"] == "https://in-toto.io/Statement/v1" and statement["predicateType"] == "https://packslip.dev/release/v1", "unexpected signed statement type")
    predicate = statement["predicate"]
    require(predicate["project"] == PROJECT and predicate["version"] == version, "wrong signed project or version")
    require(predicate["source"] == {"repo": "https://" + PROJECT, "commit": commit, "tag": "v" + version}, "wrong signed source")
    require(predicate["identity"] == {"scheme": "sigstore-oidc", "key_id": IDENTITY, "issuer": ISSUER}, "wrong signed workflow identity")
    expected = {name for name in filenames(version) if name.endswith((".tar.gz", ".zip", ".cdx.json"))}
    subjects = statement["subject"]
    require(len(subjects) == len(expected) and {s["name"] for s in subjects} == expected, "signed subjects differ from four archives and four SBOMs")
    for subject in subjects:
        require(subject["digest"]["sha256"] == sha256(root / subject["name"]), "signed digest mismatch")
    artifacts = predicate["artifacts"]
    resources = predicate["resources"]
    require(len(artifacts) == len(TARGETS) and len(resources) == len(TARGETS), "expected exactly four installable archives and SBOM resources")
    url_base = "https://" + PROJECT + "/releases/download/v" + version + "/"
    for target in TARGETS:
        target_os, target_arch = target.split("-")
        stem = "sobalink-" + version + "-" + target
        name = stem + (".zip" if target_os == "windows" else ".tar.gz")
        artifact = next(a for a in artifacts if a["name"] == name)
        require(artifact["os"] == target_os and artifact["arch"] == {"amd64": "x86_64", "arm64": "aarch64"}[target_arch], "wrong signed native platform")
        require(artifact["format"] == ("zip" if target_os == "windows" else "tar.gz"), "wrong signed archive format")
        require(artifact["bin"] == ["bin/soba" + (".exe" if target_os == "windows" else "")], "wrong signed executable path")
        # Packslip 1.4.0 normalizes libc=any to an absent libc constraint.
        require(artifact.get("libc") is None, "unexpected signed libc restriction")
        require(artifact["url"] == url_base + name and artifact["size"] == (root / name).stat().st_size, "wrong signed artifact download")
        require(artifact["provenance"] == ["https://api.github.com/repos/" + REPOSITORY + "/attestations/sha256:" + sha256(root / name)], "wrong or missing provenance link")
        resource = next(r for r in resources if r["artifact"] == name)
        require(resource["kind"] == "sbom" and resource["format"] == "cyclonedx" and resource["asset"] == stem + ".cdx.json" and resource["url"] == url_base + stem + ".cdx.json", "wrong signed SBOM binding")
    print("Verified signed project, workflow identity, source, four platforms, provenance links, and all eight signed digests")


def download_public(root, version, commit):
    _, release = release_state(version, commit, required=True, published=True)
    expected = filenames(version) | {"packslip.sigstore.json"}
    require(len(release["assets"]) == len(expected) and {a["name"] for a in release["assets"]} == expected, "published asset set differs from the verified inventory")
    root.mkdir(parents=True, exist_ok=False)
    for asset in release["assets"]:
        name = asset["name"]
        url = "https://" + PROJECT + "/releases/download/v" + version + "/" + name
        require(asset["browser_download_url"] == url and asset["state"] == "uploaded", "invalid public asset URL/state")
        # No Authorization header: prove ordinary public release downloads work.
        for attempt in range(6):
            try:
                with urllib.request.urlopen(url, timeout=120) as response, (root / name).open("wb") as output:
                    while block := response.read(1024 * 1024):
                        output.write(block)
                break
            except (urllib.error.HTTPError, urllib.error.URLError):
                if attempt == 5:
                    raise
                time.sleep(10)
        require((root / name).stat().st_size == asset["size"], "truncated public download")
        if asset.get("digest"):
            require(asset["digest"] == "sha256:" + sha256(root / name), "GitHub public asset digest mismatch")
    check_assets(root, version, commit, bundle=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("gate", "draft", "publish-ready", "assets", "bundle", "download-public"))
    parser.add_argument("--root", type=pathlib.Path, default=pathlib.Path("dist"))
    parser.add_argument("--proof", type=pathlib.Path, help="full-validation proof from this release gate")
    parser.add_argument("--revalidated-proof", type=pathlib.Path, help="write publication-time full-validation evidence")
    args = parser.parse_args()
    version, commit = os.environ["RELEASE_VERSION"], os.environ["TESTED_COMMIT"]
    validate_inputs(version, commit)
    if args.command == "gate":
        require(args.proof is not None, "gate proof output is required")
        result = source_gate(version, commit, os.environ)
        args.proof.write_text(json.dumps(result["full_validation_proof"], sort_keys=True, indent=2) + "\n", encoding="utf-8")
        release_state(version, commit)
        if os.environ.get("GITHUB_OUTPUT"):
            with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as output:
                output.write("version=" + version + "\ntag=v" + version + "\ncommit=" + commit + "\n")
    elif args.command in ("draft", "publish-ready"):
        require(api("git/ref/heads/main")["object"]["sha"] == commit, "main moved before publication; do not publish stale source")
        _, release = release_state(version, commit, required=True)
        if args.command == "publish-ready":
            require(args.proof is not None and args.revalidated_proof is not None, "publication full-CI proof paths required")
            require(args.proof.stat().st_size <= 1024 * 1024, "oversized full-CI proof")
            previous = json.loads(args.proof.read_text(encoding="utf-8"))
            require(isinstance(previous, dict), "invalid full-CI proof")
            proof = full_validation.audit(api, commit, previous_proof=previous)
            args.revalidated_proof.write_text(json.dumps(proof, sort_keys=True, indent=2) + "\n", encoding="utf-8")
            check_assets(args.root, version, commit, bundle=True)
            check_bundle(args.root, version, commit)
            require({a["name"] for a in release["assets"]} == filenames(version) | {"packslip.sigstore.json"}, "draft assets are incomplete")
            for asset in release["assets"]:
                require(asset["state"] == "uploaded" and asset["digest"] == "sha256:" + sha256(args.root / asset["name"]), "staged asset digest mismatch")
    elif args.command == "assets":
        check_assets(args.root, version, commit)
    elif args.command == "bundle":
        check_bundle(args.root, version, commit)
    else:
        download_public(args.root, version, commit)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, StopIteration) as error:
        sys.exit("Release validation failed: " + str(error))
