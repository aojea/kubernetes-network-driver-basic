# Running integration tests


1. Install `bats` https://bats-core.readthedocs.io/en/stable/installation.html

2. Install `kind` https://kind.sigs.k8s.io/

3. Ensure git submodules have been initialized: `git submodule update --init --recursive`

4. Run `bats tests/`
