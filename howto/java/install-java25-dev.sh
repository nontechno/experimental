#!/usr/bin/env bash
#
# install-java25-dev.sh
#
# Provision a Java 25 development environment on Oracle Linux (8 / 9 / 10).
# Everything is idempotent: a component is only installed when it is missing
# or older than the requested version.
#
# Installs:
#   * base tooling      git, gcc, make, curl, wget, unzip, zip, tar, jq, ...
#   * JDK 25            distro OpenJDK -> Oracle JDK (developer repo) -> Temurin tarball
#   * Gradle            latest stable from services.gradle.org (sha256 verified)
#   * Apache Maven      latest 3.9.x from dlcdn.apache.org (sha512 verified)
#   * Node.js + npm     dnf module -> NodeSource -> nodejs.org tarball
#
# Usage:  sudo ./install-java25-dev.sh [options]
# Run     ./install-java25-dev.sh --help   for the option list.
#
set -Eeuo pipefail

# --------------------------------------------------------------------------
# Configuration (override via environment or CLI flags)
# --------------------------------------------------------------------------
JAVA_MAJOR="${JAVA_MAJOR:-25}"
NODE_MAJOR="${NODE_MAJOR:-22}"
OPT_PREFIX="${OPT_PREFIX:-/opt}"
PROFILE_D="${PROFILE_D:-/etc/profile.d/java-dev.sh}"

# Used only when the "what is the latest version" lookup fails (offline mirror, etc.)
GRADLE_FALLBACK_VERSION="${GRADLE_FALLBACK_VERSION:-9.1.0}"
MAVEN_FALLBACK_VERSION="${MAVEN_FALLBACK_VERSION:-3.9.11}"

# Minimum acceptable versions for an already-installed tool
GRADLE_MIN="${GRADLE_MIN:-9.0}"
MAVEN_MIN="${MAVEN_MIN:-3.9.0}"

DRY_RUN=false
FORCE=false
DO_JDK=true
DO_GRADLE=true
DO_MAVEN=true
DO_NODE=true

# --------------------------------------------------------------------------
# Plumbing
# --------------------------------------------------------------------------
if [[ -t 1 ]]; then
    C_RED=$'\033[31m'; C_GRN=$'\033[32m'; C_YLW=$'\033[33m'
    C_BLU=$'\033[34m'; C_BLD=$'\033[1m';  C_RST=$'\033[0m'
else
    C_RED=""; C_GRN=""; C_YLW=""; C_BLU=""; C_BLD=""; C_RST=""
fi

info()  { printf '%s==>%s %s\n'      "$C_BLU" "$C_RST" "$*"; }
step()  { printf '\n%s==> %s%s\n'    "$C_BLD" "$*"      "$C_RST"; }
ok()    { printf '  %s[ok]%s %s\n'   "$C_GRN" "$C_RST" "$*"; }
warn()  { printf '  %s[warn]%s %s\n' "$C_YLW" "$C_RST" "$*" >&2; }
err()   { printf '  %s[err]%s %s\n'  "$C_RED" "$C_RST" "$*" >&2; }
die()   { err "$*"; exit 1; }

trap 'err "failed at line $LINENO: ${BASH_COMMAND}"' ERR

TMPDIR_WORK="$(mktemp -d -t java25-dev.XXXXXXXX)"
cleanup() { rm -rf "$TMPDIR_WORK"; }
trap cleanup EXIT

have() { command -v "$1" >/dev/null 2>&1; }

# ver_ge A B  ->  true when A >= B
ver_ge() { [[ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n1)" == "$2" ]]; }

run() {
    if $DRY_RUN; then
        printf '  [dry-run] %s\n' "$*"
        return 0
    fi
    "$@"
}

fetch() { curl -fsSL --retry 3 --retry-delay 2 --connect-timeout 20 "$1" -o "$2"; }
fetch_stdout() { curl -fsSL --retry 3 --retry-delay 2 --connect-timeout 20 "$1"; }

usage() {
    cat <<EOF
${C_BLD}install-java25-dev.sh${C_RST} - Java ${JAVA_MAJOR} toolchain installer for Oracle Linux

  --java-version N     JDK feature release to install   (default: ${JAVA_MAJOR})
  --node-major N       Node.js major version            (default: ${NODE_MAJOR})
  --prefix DIR         Where tarball installs go        (default: ${OPT_PREFIX})
  --skip-jdk           Do not touch the JDK
  --skip-gradle        Do not install Gradle
  --skip-maven         Do not install Maven
  --skip-node          Do not install Node.js / npm
  --force              Reinstall even if an acceptable version is present
  --dry-run            Print the privileged commands instead of running them
  -h, --help           This text

Environment overrides: JAVA_MAJOR NODE_MAJOR OPT_PREFIX PROFILE_D
                       GRADLE_MIN MAVEN_MIN
                       GRADLE_FALLBACK_VERSION MAVEN_FALLBACK_VERSION
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --java-version) JAVA_MAJOR="${2:?missing value}"; shift 2 ;;
        --node-major)   NODE_MAJOR="${2:?missing value}"; shift 2 ;;
        --prefix)       OPT_PREFIX="${2:?missing value}"; shift 2 ;;
        --skip-jdk)     DO_JDK=false;    shift ;;
        --skip-gradle)  DO_GRADLE=false; shift ;;
        --skip-maven)   DO_MAVEN=false;  shift ;;
        --skip-node)    DO_NODE=false;   shift ;;
        --force)        FORCE=true;      shift ;;
        --dry-run)      DRY_RUN=true;    shift ;;
        -h|--help)      usage; exit 0 ;;
        *)              usage; die "unknown option: $1" ;;
    esac
done

# --------------------------------------------------------------------------
# Privilege + platform detection
# --------------------------------------------------------------------------
declare -a SUDO=()
if [[ ${EUID} -ne 0 ]]; then
    have sudo || die "run as root, or install sudo"
    SUDO=(sudo)
    if ! $DRY_RUN; then
        info "requesting sudo credentials"
        sudo -v
    fi
fi

PKG=""
for candidate in dnf yum; do
    if have "$candidate"; then PKG="$candidate"; break; fi
done
[[ -n "$PKG" ]] || die "neither dnf nor yum found - is this really Oracle Linux?"

OS_ID=""; OS_VER=""
if [[ -r /etc/os-release ]]; then
    # shellcheck disable=SC1091
    . /etc/os-release
    OS_ID="${ID:-unknown}"
    OS_VER="${VERSION_ID:-0}"
fi
OS_MAJOR="${OS_VER%%.*}"

case "$OS_ID" in
    ol)                          ;;
    rhel|centos|rocky|almalinux) warn "detected ${OS_ID} ${OS_VER}; script targets Oracle Linux but should work" ;;
    fedora)                      warn "detected Fedora; repo names differ, tarball fallbacks will be used" ;;
    *)                           warn "unrecognised distribution '${OS_ID}'; proceeding anyway" ;;
esac

case "$(uname -m)" in
    x86_64)        ARCH_TEMURIN=x64;      ARCH_NODE=x64     ;;
    aarch64|arm64) ARCH_TEMURIN=aarch64;  ARCH_NODE=arm64   ;;
    *)             die "unsupported architecture: $(uname -m)" ;;
esac

info "distribution : ${OS_ID} ${OS_VER} (${PKG})"
info "architecture : $(uname -m)"
info "target JDK   : ${JAVA_MAJOR}"
$DRY_RUN && warn "dry-run mode: no changes will be made"

# --------------------------------------------------------------------------
# Package helpers
# --------------------------------------------------------------------------
rpm_installed() { rpm -q "$1" >/dev/null 2>&1; }

pkg_exists() { "$PKG" -q info "$1" >/dev/null 2>&1; }

pkg_install() {
    local -a missing=()
    local p
    for p in "$@"; do
        rpm_installed "$p" || missing+=("$p")
    done
    if [[ ${#missing[@]} -eq 0 ]]; then
        ok "already present: $*"
        return 0
    fi
    info "installing: ${missing[*]}"
    run "${SUDO[@]}" "$PKG" -y install "${missing[@]}"
}

# --------------------------------------------------------------------------
# 1. Base tooling
# --------------------------------------------------------------------------
install_base_tools() {
    step "base development tooling"

    local -a wanted=(
        ca-certificates curl wget tar gzip bzip2 xz zip unzip
        git make gcc gcc-c++ patch findutils which procps-ng jq
    )
    local -a available=()
    local p
    for p in "${wanted[@]}"; do
        if rpm_installed "$p" || pkg_exists "$p"; then
            available+=("$p")
        else
            warn "package not in any enabled repo, skipping: $p"
        fi
    done
    pkg_install "${available[@]}"
}

# --------------------------------------------------------------------------
# 2. JDK
# --------------------------------------------------------------------------
# Extract the feature release number from a `java -version` / `javac -version`
# style string, coping with both "25.0.1" and legacy "1.8.0_402".
parse_java_major() {
    local raw="$1" v=""
    # `java -version` style:   openjdk version "25.0.1" 2025-10-21
    v="$(sed -nE 's/.*version "([^"]+)".*/\1/p' <<<"$raw" | head -n1)"
    # `javac -version` style:  javac 25.0.1
    [[ -n "$v" ]] || v="$(sed -nE 's/^javac[[:space:]]+([0-9][0-9._]*).*/\1/p' <<<"$raw" | head -n1)"
    # last resort: first version-looking token on the line
    [[ -n "$v" ]] || v="$(grep -oE '[0-9]+(\.[0-9]+)*(_[0-9]+)?' <<<"$raw" | head -n1)"
    [[ -n "$v" ]] || return 1
    if [[ "$v" == 1.* ]]; then
        cut -d. -f2 <<<"$v"
    else
        printf '%s\n' "${v%%.*}"
    fi
}

current_javac_major() {
    have javac || return 1
    parse_java_major "$(javac -version 2>&1 | head -n1)"
}

java_home_from_javac() {
    local jc bin
    jc="$(readlink -f "$(command -v javac)")" || return 1
    bin="$(dirname "$jc")"
    dirname "$bin"
}

register_java_alternatives() {
    local home="$1"
    local prio="${2:-20000}"
    local -a slaves=()
    local t
    for t in javac jar jarsigner javadoc javap jshell jcmd jstack jps jlink jpackage keytool jdeps; do
        [[ -x "${home}/bin/${t}" ]] && slaves+=(--slave "/usr/bin/${t}" "$t" "${home}/bin/${t}")
    done
    [[ -x "${home}/bin/java" ]] || die "no java binary under ${home}/bin"

    run "${SUDO[@]}" alternatives --install /usr/bin/java java "${home}/bin/java" "$prio" "${slaves[@]}"
    run "${SUDO[@]}" alternatives --set java "${home}/bin/java"
}

install_jdk_from_repo() {
    # Try the distro OpenJDK build first, then the Oracle JDK rpm.
    local pkg
    for pkg in "java-${JAVA_MAJOR}-openjdk-devel" "jdk-${JAVA_MAJOR}"; do
        if pkg_exists "$pkg"; then
            info "found ${pkg} in the enabled repositories"
            pkg_install "$pkg"
            return 0
        fi
    done

    # Oracle ships JDK rpms in the developer channel - try enabling it once.
    local devrepo="ol${OS_MAJOR}_developer"
    if [[ "$OS_ID" == "ol" ]] && have dnf; then
        if dnf repolist --all 2>/dev/null | grep -q "^${devrepo}[[:space:]]"; then
            info "enabling ${devrepo} and retrying"
            run "${SUDO[@]}" dnf config-manager --set-enabled "$devrepo" || true
            for pkg in "jdk-${JAVA_MAJOR}" "java-${JAVA_MAJOR}-openjdk-devel"; do
                if pkg_exists "$pkg"; then
                    pkg_install "$pkg"
                    return 0
                fi
            done
        fi
    fi
    return 1
}

install_jdk_from_temurin() {
    local dest="${OPT_PREFIX}/java/jdk-${JAVA_MAJOR}"
    local url="https://api.adoptium.net/v3/binary/latest/${JAVA_MAJOR}/ga/linux/${ARCH_TEMURIN}/jdk/hotspot/normal/eclipse"
    local tgz="${TMPDIR_WORK}/temurin-${JAVA_MAJOR}.tar.gz"

    info "downloading Eclipse Temurin JDK ${JAVA_MAJOR} (${ARCH_TEMURIN})"
    if $DRY_RUN; then
        printf '  [dry-run] fetch %s -> %s\n' "$url" "$dest"
        JAVA_HOME_INSTALLED="$dest"
        return 0
    fi

    fetch "$url" "$tgz" || return 1
    tar -tzf "$tgz" >/dev/null 2>&1 || { err "downloaded archive is not a valid tarball"; return 1; }

    "${SUDO[@]}" rm -rf "$dest"
    "${SUDO[@]}" mkdir -p "$dest"
    "${SUDO[@]}" tar -xzf "$tgz" -C "$dest" --strip-components=1
    JAVA_HOME_INSTALLED="$dest"
    return 0
}

install_jdk() {
    step "JDK ${JAVA_MAJOR}"
    JAVA_HOME_INSTALLED=""

    local cur=""
    cur="$(current_javac_major || true)"
    if [[ -n "$cur" ]] && ! $FORCE; then
        if ver_ge "$cur" "$JAVA_MAJOR"; then
            JAVA_HOME_INSTALLED="$(java_home_from_javac)"
            ok "JDK ${cur} already installed at ${JAVA_HOME_INSTALLED}"
            return 0
        fi
        info "found JDK ${cur}, which is older than ${JAVA_MAJOR}"
    fi

    if install_jdk_from_repo; then
        if ! $DRY_RUN; then
            # rpm installs land under /usr/lib/jvm - pick the requested release.
            local home
            home="$(find /usr/lib/jvm -maxdepth 1 -type d \
                     \( -name "*-${JAVA_MAJOR}-*" -o -name "jdk-${JAVA_MAJOR}*" -o -name "java-${JAVA_MAJOR}*" \) \
                     2>/dev/null | sort | tail -n1)"
            if [[ -n "$home" && -x "${home}/bin/javac" ]]; then
                JAVA_HOME_INSTALLED="$home"
            else
                JAVA_HOME_INSTALLED="$(java_home_from_javac || true)"
            fi
        fi
    else
        warn "no JDK ${JAVA_MAJOR} rpm available; falling back to Eclipse Temurin"
        install_jdk_from_temurin || die "could not install a JDK ${JAVA_MAJOR}"
        register_java_alternatives "$JAVA_HOME_INSTALLED"
    fi

    if ! $DRY_RUN; then
        [[ -n "${JAVA_HOME_INSTALLED}" && -x "${JAVA_HOME_INSTALLED}/bin/javac" ]] \
            || die "JDK install finished but javac was not found"
        ok "JAVA_HOME = ${JAVA_HOME_INSTALLED}"
        ok "$("${JAVA_HOME_INSTALLED}/bin/java" -version 2>&1 | head -n1)"
    fi
}

# --------------------------------------------------------------------------
# 3. Gradle
# --------------------------------------------------------------------------
gradle_latest_version() {
    local json
    json="$(fetch_stdout https://services.gradle.org/versions/current 2>/dev/null)" || return 1
    if have jq; then
        jq -r '.version // empty' <<<"$json"
    else
        sed -nE 's/.*"version"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p' <<<"$json" | head -n1
    fi
}

install_gradle() {
    step "Gradle"

    if have gradle && ! $FORCE; then
        local cur
        cur="$(gradle --version 2>/dev/null | sed -nE 's/^Gradle[[:space:]]+([0-9][^[:space:]]*)/\1/p' | head -n1)"
        if [[ -n "$cur" ]] && ver_ge "$cur" "$GRADLE_MIN"; then
            ok "Gradle ${cur} already installed ($(command -v gradle))"
            GRADLE_HOME_INSTALLED="$(dirname "$(dirname "$(readlink -f "$(command -v gradle)")")")"
            return 0
        fi
        [[ -n "$cur" ]] && info "found Gradle ${cur} (< ${GRADLE_MIN}), upgrading"
    fi

    local ver
    ver="$(gradle_latest_version || true)"
    if [[ -z "$ver" ]]; then
        warn "version lookup failed, using fallback ${GRADLE_FALLBACK_VERSION}"
        ver="$GRADLE_FALLBACK_VERSION"
    fi
    info "installing Gradle ${ver}"

    local base="${OPT_PREFIX}/gradle"
    local dest="${base}/gradle-${ver}"

    if $DRY_RUN; then
        printf '  [dry-run] install gradle %s into %s\n' "$ver" "$dest"
        GRADLE_HOME_INSTALLED="${base}/current"
        return 0
    fi

    local url="https://services.gradle.org/distributions/gradle-${ver}-bin.zip"
    local zipf="${TMPDIR_WORK}/gradle-${ver}.zip"

    fetch "$url" "$zipf" || die "failed to download ${url}"

    local sum
    if sum="$(fetch_stdout "${url}.sha256" 2>/dev/null)" && [[ -n "$sum" ]]; then
        printf '%s  %s\n' "$sum" "$zipf" | sha256sum -c - >/dev/null \
            || die "checksum mismatch for gradle-${ver}-bin.zip"
        ok "sha256 verified"
    else
        warn "no published sha256 - skipping integrity check"
    fi

    "${SUDO[@]}" mkdir -p "$base"
    "${SUDO[@]}" rm -rf "$dest"
    "${SUDO[@]}" unzip -q "$zipf" -d "$base"
    "${SUDO[@]}" ln -sfn "$dest" "${base}/current"

    GRADLE_HOME_INSTALLED="${base}/current"
    "${SUDO[@]}" ln -sfn "${base}/current/bin/gradle" /usr/local/bin/gradle
    ok "gradle ${ver} -> ${base}/current"
}

# --------------------------------------------------------------------------
# 4. Maven
# --------------------------------------------------------------------------
maven_latest_version() {
    fetch_stdout https://dlcdn.apache.org/maven/maven-3/ 2>/dev/null \
        | grep -oE '3\.[0-9]+\.[0-9]+/' | tr -d '/' | sort -V | uniq | tail -n1
}

install_maven() {
    step "Apache Maven"

    if have mvn && ! $FORCE; then
        local cur
        cur="$(mvn -v 2>/dev/null | sed -nE 's/^Apache Maven ([0-9][^[:space:]]*).*/\1/p' | head -n1)"
        if [[ -n "$cur" ]] && ver_ge "$cur" "$MAVEN_MIN"; then
            ok "Maven ${cur} already installed ($(command -v mvn))"
            MAVEN_HOME_INSTALLED="$(dirname "$(dirname "$(readlink -f "$(command -v mvn)")")")"
            return 0
        fi
        [[ -n "$cur" ]] && info "found Maven ${cur} (< ${MAVEN_MIN}), upgrading"
    fi

    local ver
    ver="$(maven_latest_version || true)"
    if [[ -z "$ver" ]]; then
        warn "version lookup failed, using fallback ${MAVEN_FALLBACK_VERSION}"
        ver="$MAVEN_FALLBACK_VERSION"
    fi
    info "installing Maven ${ver}"

    local base="${OPT_PREFIX}/maven"
    local dest="${base}/apache-maven-${ver}"

    if $DRY_RUN; then
        printf '  [dry-run] install maven %s into %s\n' "$ver" "$dest"
        MAVEN_HOME_INSTALLED="${base}/current"
        return 0
    fi

    local url="https://dlcdn.apache.org/maven/maven-3/${ver}/binaries/apache-maven-${ver}-bin.tar.gz"
    local tgz="${TMPDIR_WORK}/maven-${ver}.tar.gz"

    fetch "$url" "$tgz" || die "failed to download ${url}"

    local sum
    if sum="$(fetch_stdout "https://downloads.apache.org/maven/maven-3/${ver}/binaries/apache-maven-${ver}-bin.tar.gz.sha512" 2>/dev/null)" \
       && [[ -n "$sum" ]]; then
        printf '%s  %s\n' "$(tr -d '[:space:]' <<<"$sum")" "$tgz" | sha512sum -c - >/dev/null \
            || die "checksum mismatch for apache-maven-${ver}-bin.tar.gz"
        ok "sha512 verified"
    else
        warn "no published sha512 - skipping integrity check"
    fi

    "${SUDO[@]}" mkdir -p "$base"
    "${SUDO[@]}" rm -rf "$dest"
    "${SUDO[@]}" tar -xzf "$tgz" -C "$base"
    "${SUDO[@]}" ln -sfn "$dest" "${base}/current"

    MAVEN_HOME_INSTALLED="${base}/current"
    "${SUDO[@]}" ln -sfn "${base}/current/bin/mvn" /usr/local/bin/mvn
    ok "mvn ${ver} -> ${base}/current"
}

# --------------------------------------------------------------------------
# 5. Node.js + npm
# --------------------------------------------------------------------------
node_from_module() {
    have dnf || return 1
    "$PKG" module list nodejs 2>/dev/null \
        | grep -qE "(^|[[:space:]])nodejs[[:space:]]+${NODE_MAJOR}([[:space:]]|$)" || return 1

    info "using the nodejs:${NODE_MAJOR} dnf module stream"
    run "${SUDO[@]}" dnf -y module reset nodejs
    run "${SUDO[@]}" dnf -y module enable "nodejs:${NODE_MAJOR}"
    run "${SUDO[@]}" dnf -y install nodejs npm
}

node_from_nodesource() {
    info "using the NodeSource repository for Node ${NODE_MAJOR}"
    if $DRY_RUN; then
        printf '  [dry-run] curl -fsSL https://rpm.nodesource.com/setup_%s.x | bash - && %s -y install nodejs\n' \
            "$NODE_MAJOR" "$PKG"
        return 0
    fi
    have dnf && "${SUDO[@]}" dnf -y module reset nodejs >/dev/null 2>&1 || true
    curl -fsSL "https://rpm.nodesource.com/setup_${NODE_MAJOR}.x" | "${SUDO[@]}" bash - || return 1
    "${SUDO[@]}" "$PKG" -y install nodejs
}

node_from_tarball() {
    local base="${OPT_PREFIX}/node"
    info "falling back to the official nodejs.org tarball"
    if $DRY_RUN; then
        printf '  [dry-run] install node %s.x tarball into %s\n' "$NODE_MAJOR" "$base"
        return 0
    fi

    local index file url tgz
    index="$(fetch_stdout "https://nodejs.org/dist/latest-v${NODE_MAJOR}.x/" 2>/dev/null)" || return 1
    file="$(grep -oE "node-v[0-9.]+-linux-${ARCH_NODE}\.tar\.xz" <<<"$index" | head -n1)"
    [[ -n "$file" ]] || return 1

    url="https://nodejs.org/dist/latest-v${NODE_MAJOR}.x/${file}"
    tgz="${TMPDIR_WORK}/${file}"
    fetch "$url" "$tgz" || return 1

    "${SUDO[@]}" mkdir -p "$base"
    "${SUDO[@]}" rm -rf "${base}/current"
    "${SUDO[@]}" mkdir -p "${base}/current"
    "${SUDO[@]}" tar -xJf "$tgz" -C "${base}/current" --strip-components=1

    local b
    for b in node npm npx; do
        [[ -e "${base}/current/bin/${b}" ]] && "${SUDO[@]}" ln -sfn "${base}/current/bin/${b}" "/usr/local/bin/${b}"
    done
    NODE_HOME_INSTALLED="${base}/current"
}

install_node() {
    step "Node.js ${NODE_MAJOR}.x + npm"
    NODE_HOME_INSTALLED=""

    if have node && have npm && ! $FORCE; then
        local cur
        cur="$(node --version 2>/dev/null | tr -d 'v')"
        if [[ -n "$cur" ]] && ver_ge "${cur%%.*}" "$NODE_MAJOR"; then
            ok "Node ${cur} / npm $(npm --version 2>/dev/null) already installed"
            return 0
        fi
        [[ -n "$cur" ]] && info "found Node ${cur}, want >= ${NODE_MAJOR}"
    fi

    node_from_module || node_from_nodesource || node_from_tarball \
        || die "could not install Node.js ${NODE_MAJOR}"

    if ! $DRY_RUN; then
        hash -r
        have node || die "node install reported success but the binary is missing"
        ok "node $(node --version), npm $(npm --version 2>/dev/null || echo '<missing>')"
    fi
}

# --------------------------------------------------------------------------
# 6. Environment file
# --------------------------------------------------------------------------
write_profile() {
    step "environment (${PROFILE_D})"

    local java_home="${JAVA_HOME_INSTALLED:-}"
    local gradle_home="${GRADLE_HOME_INSTALLED:-}"
    local maven_home="${MAVEN_HOME_INSTALLED:-}"
    local node_home="${NODE_HOME_INSTALLED:-}"

    local content
    content="# Managed by install-java25-dev.sh - edits will be overwritten.
"
    [[ -n "$java_home"   ]] && content+="export JAVA_HOME=\"${java_home}\"
export PATH=\"\${JAVA_HOME}/bin:\${PATH}\"
"
    [[ -n "$gradle_home" ]] && content+="export GRADLE_HOME=\"${gradle_home}\"
export PATH=\"\${GRADLE_HOME}/bin:\${PATH}\"
"
    [[ -n "$maven_home"  ]] && content+="export MAVEN_HOME=\"${maven_home}\"
export M2_HOME=\"${maven_home}\"
export PATH=\"\${MAVEN_HOME}/bin:\${PATH}\"
"
    [[ -n "$node_home"   ]] && content+="export PATH=\"${node_home}/bin:\${PATH}\"
"

    if $DRY_RUN; then
        printf '  [dry-run] would write:\n%s\n' "$content" | sed 's/^/    /'
        return 0
    fi

    printf '%s' "$content" | "${SUDO[@]}" tee "$PROFILE_D" >/dev/null
    "${SUDO[@]}" chmod 0644 "$PROFILE_D"
    ok "wrote ${PROFILE_D}"
}

# --------------------------------------------------------------------------
# 7. Summary
# --------------------------------------------------------------------------
summary() {
    step "summary"
    $DRY_RUN && { warn "dry-run: nothing was installed"; return 0; }

    # Make the freshly written PATH visible to this shell for the version probe.
    # shellcheck disable=SC1090
    [[ -r "$PROFILE_D" ]] && . "$PROFILE_D"
    hash -r

    local -a tools=(java javac jshell gradle mvn node npm git)
    local t line
    for t in "${tools[@]}"; do
        if have "$t"; then
            case "$t" in
                java)   line="$(java -version 2>&1 | head -n1)" ;;
                javac)  line="$(javac -version 2>&1 | head -n1)" ;;
                jshell) line="jshell $(jshell --version 2>&1 | head -n1)" ;;
                gradle) line="Gradle $(gradle --version 2>/dev/null | sed -nE 's/^Gradle[[:space:]]+(.*)/\1/p' | head -n1)" ;;
                mvn)    line="$(mvn -v 2>/dev/null | head -n1)" ;;
                node)   line="node $(node --version)" ;;
                npm)    line="npm $(npm --version 2>/dev/null)" ;;
                git)    line="$(git --version)" ;;
            esac
            printf '  %-8s %s%s%s\n' "$t" "$C_GRN" "$line" "$C_RST"
        else
            printf '  %-8s %s%s%s\n' "$t" "$C_YLW" "not installed" "$C_RST"
        fi
    done

    cat <<EOF

Open a new login shell, or run:

    source ${PROFILE_D}

Quick check:

    jshell --enable-preview -  <<< 'System.out.println("Java " + Runtime.version());'
EOF
}

# --------------------------------------------------------------------------
main() {
    JAVA_HOME_INSTALLED=""; GRADLE_HOME_INSTALLED=""
    MAVEN_HOME_INSTALLED=""; NODE_HOME_INSTALLED=""

    install_base_tools
    $DO_JDK    && install_jdk
    $DO_GRADLE && install_gradle
    $DO_MAVEN  && install_maven
    $DO_NODE   && install_node
    write_profile
    summary
}

main "$@"
