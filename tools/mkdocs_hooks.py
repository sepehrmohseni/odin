"""MkDocs hooks for the Odin documentation site (see mkdocs.yml).

- Publishes ROADMAP.md, CHANGELOG.md and CONTRIBUTING.md from the repository root as pages.
- Rewrites links written for browsing the repository on GitHub: a link to a published page or
  file stays on the site; a link to any other repository file goes to that file on GitHub. Broken
  links are still reported, and fail the strict build.
"""

import os
import posixpath
import re

from mkdocs.structure.files import File

REPO = "https://github.com/sepehrmohseni/odin"
ROOT_PAGES = {
    "ROADMAP.md": "roadmap.md",
    "CHANGELOG.md": "changelog.md",
    "CONTRIBUTING.md": "contributing.md",
}
LINK = re.compile(r"(\]\()([^)\s]+)(\))")


def on_files(files, config):
    root = os.path.dirname(config["config_file_path"])
    for source, page in ROOT_PAGES.items():
        with open(os.path.join(root, source), encoding="utf-8") as f:
            files.append(File.generated(config, page, content=f.read()))
    return files


def on_page_markdown(markdown, page, config, files):
    src = page.file.src_uri
    # Where the page lives in the repository, to resolve its relative links.
    repo_path = next((k for k, v in ROOT_PAGES.items() if v == src), "docs/" + src)
    # Every file the site publishes (pages, images, other assets), so a relative link to an
    # image stays on the site instead of going to GitHub.
    published = {f.src_uri for f in files if f.inclusion.is_included()}

    def rewrite(match):
        target = match.group(2)
        if re.match(r"^[a-z]+:", target) or target.startswith(("#", "/")):
            return match.group(0)
        path, _, anchor = target.partition("#")
        anchor = "#" + anchor if anchor else ""
        resolved = posixpath.normpath(posixpath.join(posixpath.dirname(repo_path), path))

        site_path = None
        if resolved in ROOT_PAGES:
            site_path = ROOT_PAGES[resolved]
        elif resolved.startswith("docs/") and resolved[len("docs/"):] in published:
            site_path = resolved[len("docs/"):]
        if site_path is not None:
            rel = posixpath.relpath(site_path, posixpath.dirname(src) or ".")
            return f"{match.group(1)}{rel}{anchor}{match.group(3)}"

        if resolved.startswith("docs/") and resolved.endswith(".md"):
            return match.group(0)  # a missing page: leave it for the strict build to report
        kind = "tree" if os.path.isdir(os.path.join(os.path.dirname(config["config_file_path"]), resolved)) else "blob"
        return f"{match.group(1)}{REPO}/{kind}/main/{resolved}{anchor}{match.group(3)}"

    return LINK.sub(rewrite, markdown)
