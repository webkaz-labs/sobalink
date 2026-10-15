"""Verify that packaged documentation's local images are readable regular files.

This deliberately needs no Markdown renderer or network access. Check the root
Markdown/HTML documents and the docs tree, including picture/source alternatives.
"""
import html
from html.parser import HTMLParser
import os
import pathlib
import re
import stat
from urllib.parse import unquote, urlsplit


class ImageHTMLParser(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.targets = []

    def handle_starttag(self, tag, attrs):
        if tag not in ("img", "source"):
            return
        for name, value in attrs:
            if name == "src" and value:
                self.targets.append(value)
            elif name == "srcset" and value:
                self.targets.extend(_srcset_targets(value))

    handle_startendtag = handle_starttag


def _srcset_targets(value):
    # Separate URL tokens from descriptors without splitting a data URL's comma.
    while value:
        value = value.lstrip(" \t\n\r\f,")
        if not value:
            break
        pattern = r"[^\s]+" if value.lower().startswith("data:") else r"[^\s,]+"
        match = re.match(pattern, value)
        target = match[0]
        value = value[match.end():]
        yield target.rstrip(",")
        if target.endswith(","):
            continue
        # Ignore width/density descriptors through the next candidate separator.
        separator = value.find(",")
        value = value[separator + 1:] if separator >= 0 else ""


def _prose(text):
    """Skip fenced/inline examples and HTML comments, which do not load images."""
    text = re.sub(r"<!--.*?-->", "", text, flags=re.S)
    lines = []
    fence = None
    for line in text.splitlines(keepends=True):
        match = re.match(r"^ {0,3}(`{3,}|~{3,})", line)
        if fence:
            if match and match[1][0] == fence[0] and len(match[1]) >= len(fence):
                fence = None
            continue
        if match:
            fence = match[1]
        else:
            lines.append(line)
    return re.sub(r"(`+)(?!`)(.*?)\1(?!`)", "", "".join(lines), flags=re.S)


def _destination(text):
    text = text.lstrip()
    if text.startswith("<"):
        end = text.find(">")
        return text[1:end] if end >= 0 else ""
    result = []
    depth = 0
    escaped = False
    for char in text:
        if escaped:
            result.append(char)
            escaped = False
        elif char == "\\":
            escaped = True
        elif char == "(":
            depth += 1
            result.append(char)
        elif char == ")":
            if depth == 0:
                break
            depth -= 1
            result.append(char)
        elif char.isspace():
            break
        else:
            result.append(char)
    return "".join(result)


def image_targets(text):
    text = _prose(text)
    parser = ImageHTMLParser()
    parser.feed(text)
    targets = list(parser.targets)
    label_key = lambda value: " ".join(value.split()).casefold()
    definitions = {}
    for match in re.finditer(r"^ {0,3}\[([^\]\n]+)\]:\s*(.+)$", text, re.M):
        definitions.setdefault(label_key(match[1]), _destination(match[2]))
    for match in re.finditer(r"(?<!\\)!\[((?:\\.|[^\]\\])*)\]", text):
        tail = text[match.end():]
        if tail.startswith("("):
            targets.append(_destination(tail[1:]))
        elif tail.startswith("["):
            end = tail.find("]")
            label = tail[1:end] or match[1] if end >= 0 else match[1]
            key = label_key(label)
            if key not in definitions:
                raise ValueError(f"undefined image reference: {label}")
            targets.append(definitions[key])
        elif label_key(match[1]) in definitions:
            targets.append(definitions[label_key(match[1])])
    return targets


def _regular(root, relative):
    current = root
    for part in relative.parts:
        current = current / part
        mode = current.lstat().st_mode
        if stat.S_ISLNK(mode):
            raise ValueError(f"symlink in documentation path: {relative.as_posix()}")
        if current != root / relative and not stat.S_ISDIR(mode):
            raise ValueError(f"non-directory in documentation path: {relative.as_posix()}")
    if not stat.S_ISREG(current.lstat().st_mode):
        raise ValueError(f"not a regular documentation file: {relative.as_posix()}")
    return current.read_bytes()


def _local_path(root, document, target):
    target = html.unescape(target).strip()
    if "\\" in target or re.match(r"^[A-Za-z]:", target) or any(ord(c) < 32 for c in target):
        raise ValueError(f"unsafe image path: {target!r}")
    url = urlsplit(target)
    if url.scheme or url.netloc:
        if url.scheme.lower() == "file":
            raise ValueError(f"unsafe image path: {target!r}")
        return None
    path = unquote(url.path, errors="strict")
    if not path or path.startswith("/") or "\\" in path or ":" in path or any(ord(c) < 32 for c in path):
        raise ValueError(f"unsafe image path: {target!r}")
    parts = list(document.parent.parts)
    for part in path.split("/"):
        if part == "..":
            if not parts:
                raise ValueError(f"image path escapes share root: {target!r}")
            parts.pop()
        elif part not in ("", "."):
            parts.append(part)
            if root.joinpath(*parts).is_symlink():
                raise ValueError(f"symlink in image path: {target!r}")
    return pathlib.Path(*parts)


def verify_document_images(share):
    """Raise ValueError/OSError on missing, unsafe or unreadable local images.

    Return the number of local image references checked, including alternatives.
    No trust is placed in an asset list: every target is opened and read.
    """
    root = pathlib.Path(share).absolute()
    if root.is_symlink() or not root.is_dir():
        raise ValueError("documentation share root must be a real directory")
    documents = [entry.relative_to(root) for entry in root.iterdir()
                 if entry.suffix.lower() in (".md", ".html", ".htm")]
    web = root / "web"
    if web.is_symlink():
        raise ValueError("symlink in documentation path: web")
    if web.exists():
        documents.extend(entry.relative_to(root) for entry in web.iterdir()
                         if entry.suffix.lower() in (".md", ".html", ".htm"))
    docs = root / "docs"
    if docs.is_symlink():
        raise ValueError("symlink in documentation path: docs")
    if docs.exists():
        for directory, directories, files in os.walk(docs, followlinks=False):
            for name in directories + files:
                entry = pathlib.Path(directory) / name
                if entry.is_symlink():
                    raise ValueError(f"symlink in documentation path: {entry.relative_to(root)}")
            documents.extend((pathlib.Path(directory) / name).relative_to(root)
                             for name in files if pathlib.Path(name).suffix.lower() in (".md", ".html", ".htm"))
    checked = 0
    for document in sorted(documents):
        for target in image_targets(_regular(root, document).decode("utf-8")):
            try:
                relative = _local_path(root, document, target)
                if relative is not None:
                    if not _regular(root, relative):
                        raise ValueError("empty image file")
                    checked += 1
            except (OSError, ValueError) as error:
                raise ValueError(f"{document.as_posix()}: image {target!r}: {error}") from error
    return checked


if __name__ == "__main__":
    import sys
    print(f"Verified {verify_document_images(sys.argv[1])} local documentation image references")
