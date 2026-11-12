"""Collect upstream licenses for a Hashinex binary release."""

import shutil
import subprocess
import sys
from pathlib import Path


def collect(source: Path, target: Path) -> None:
    names = ("LICENSE", "LICENCE", "NOTICE", "COPYING", "PATENTS")
    target.mkdir(parents=True, exist_ok=True)
    for path in source.iterdir():
        if path.is_file() and path.name.startswith(names):
            shutil.copyfile(path, target / path.name)
    if not any(target.iterdir()):
        raise RuntimeError(f"no license found for {target.name}")


def main() -> None:
    out = Path(sys.argv[1])
    modules = subprocess.check_output(
        [
            "go",
            "list",
            "-deps",
            "-f",
            "{{with .Module}}{{if not .Main}}{{.Path}}|{{.Version}}|{{.Dir}}{{end}}{{end}}",
            "./cmd/hashinex",
        ],
        text=True,
    )
    for line in sorted(set(modules.splitlines()) - {""}):
        name, version, directory = line.split("|")
        collect(Path(directory), out / f"{name.replace('/', '_')}@{version}")
    goroot = subprocess.check_output(["go", "env", "GOROOT"], text=True).strip()
    collect(Path(goroot), out / "go-runtime")


if __name__ == "__main__":
    main()
