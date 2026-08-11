#!/usr/bin/env bash
#
# uninstall-java25-dev.sh
#
# Reverses install-java25-dev.sh on Oracle Linux (8 / 9 / 10).
#
# Removes, when present:
#   * JDK rpms          java-<N>-openjdk*, jdk-<N> (only the targeted release
#                       unless --all-jdks is given)
#   * tarball installs  <prefix>/java, <prefix>/gradle, <prefix>/maven, <prefix>/node
#   * alternatives      java/javac/... entries pointing into <prefix>
#   * symlinks          /usr/local/bin/{gradle,mvn,node,npm,npx} that resolve into <prefix>
#   * Node.js           nodejs/npm rpms, the NodeSource repo, the dnf module stream
#   * environment       /etc/profile.d/java-dev.sh
#   * optionally        ~/.gradle ~/.m2 ~/.npm and global node_modules (--purge-user-data)
#
# It deliberately does NOT remove shared build tooling (git, gcc, make, curl,
# unzip, jq, ...) - those are used by plenty of other things on the box.
#
# The script always prints a full inventory and asks for confirmation before
# deleting anything. Use --dry-run to inspect without being asked.
#
set -Eeuo pipefail

# --------------------------------------------------------------------------
# Configuration
# --------------------------------------------------------------------------
JAVA_MAJOR="${JAVA_MAJOR:-25}"
OPT_PREFIX="${OPT_PREFIX:-/opt}"
PROFILE_D="${PROFILE_D:-/etc/profile.d/java-dev.sh}"

DRY_RUN=false
ASSUME_YES=false
ALL_JDKS=false
PURGE_USER_DATA=false
KEEP_NODE=false
KEEP_RPMS=false

# --------------------------------------------------------------------------
# Plumbing
# --------------------------------------------------------------------------
if [[ -t 1 ]]; then
    C_RED=$'\033[31m'; C_GRN=$'\033[32m'; C_YLW=$'\033[33m'
    C_BLU=$'\033[34m'; C_BLD=$'\033[1m';  C_RST=$'\033[0m'
else
    C_RED=""; C_GRN=""; C_YLW=""; C_BLU=""; C_BLD=""; C_RST=""
fi

info() { printf '%s==>%s %s\n'      "$C_BLU" "$C_RST" "$*"; }
step() { printf '\n%s==> %s%s\n'    "$C_BLD" "$*"      "$C_RST"; }
ok()   { printf '  %s[ok]%s %s\n'   "$C_GRN" "$C_RST" "$*"; }
warn() { printf '  %s[warn]%s %s\n' "$C_YLW" "$C_RST" "$*" >&2; }
err()  { printf '  %s[err]%s %s\n'  "$C_RED" "$C_RST" "$*" >&2; }
die()  { err "$*"; exit 1; }

trap 'err "failed at line $LINENO: ${BASH_COMMAND}"' ERR

have() { command -v "$1" >/dev/null 2>&1; }

run() {
    if $DRY_RUN; then
        printf '  [dry-run] %s\n' "$*"
        return 0
    fi
    "$@"
}

usage() {
    cat <<EOF
${C_BLD}uninstall-java25-dev.sh${C_RST} - remove what install-java25-dev.sh installed

  --java-version N     JDK feature release to target      (default: ${JAVA_MAJOR})
  --prefix DIR         Prefix used at install time        (default: ${OPT_PREFIX})
  --all-jdks           Remove every OpenJDK/Oracle JDK rpm, not just ${JAVA_MAJOR}
  --purge-user-data    Also delete ~/.gradle ~/.m2 ~/.npm and global node_modules
  --keep-node          Leave Node.js / npm alone
  --keep-rpms          Only undo tarball installs, symlinks and the profile file
  --dry-run            Show what would be removed, change nothing
  -y, --yes            Do not prompt for confirmation
  -h, --help           This text

Shared tooling (git, gcc, make, curl, unzip, jq, ...) is never removed.
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --java-version)    JAVA_MAJOR="${2:?missing value}"; shift 2 ;;
        --prefix)          OPT_PREFIX="${2:?missing value}"; shift 2 ;;
        --all-jdks)        ALL_JDKS=true;        shift ;;
        --purge-user-data) PURGE_USER_DATA=true; shift ;;
        --keep-node)       KEEP_NODE=true;       shift ;;
        --keep-rpms)       KEEP_RPMS=true;       shift ;;
        --dry-run)         DRY_RUN=true;         shift ;;
        -y|--yes)          ASSUME_YES=true;      shift ;;
        -h|--help)         usage; exit 0 ;;
        *)                 usage; die "unknown option: $1" ;;
    esac
done

OPT_PREFIX="${OPT_PREFIX%/}"
[[ "$OPT_PREFIX" == /* ]] || die "--prefix must be an absolute path"
[[ "$OPT_PREFIX" != "/" ]] || die "--prefix / is not acceptable"

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
[[ -n "$PKG" ]] || die "neither dnf nor yum found"

# The invoking user's home, even when running under sudo.
TARGET_HOME="${HOME}"
if [[ -n "${SUDO_USER:-}" ]]; then
    TARGET_HOME="$(getent passwd "$SUDO_USER" | cut -d: -f6)"
    [[ -n "$TARGET_HOME" ]] || TARGET_HOME="$HOME"
fi

# --------------------------------------------------------------------------
# Guarded deletion
# --------------------------------------------------------------------------
# Nothing outside these roots can ever be deleted by this script.
declare -a REMOVABLE_ROOTS=(
    "${OPT_PREFIX}/java"
    "${OPT_PREFIX}/gradle"
    "${OPT_PREFIX}/maven"
    "${OPT_PREFIX}/node"
    "/usr/local/bin"
    "/usr/local/lib/node_modules"
    "/usr/lib/node_modules"
    "/etc/profile.d"
    "/etc/yum.repos.d"
    "${TARGET_HOME}"
)

path_is_removable() {
    local p="$1" root
    [[ "$p" == /* ]] || return 1
    [[ "$p" == *".."* ]] && return 1
    # never a bare top-level directory
    [[ "$(tr -cd '/' <<<"$p" | wc -c)" -ge 2 ]] || return 1
    # never the home directory itself, only things inside it
    [[ "${p%/}" == "${TARGET_HOME%/}" ]] && return 1
    for root in "${REMOVABLE_ROOTS[@]}"; do
        [[ "$p" == "$root" || "$p" == "$root"/* ]] && return 0
    done
    return 1
}

safe_rm() {
    local p="$1"
    [[ -n "$p" ]] || return 0
    if ! path_is_removable "$p"; then
        warn "refusing to delete path outside the managed roots: ${p}"
        return 1
    fi
    if [[ ! -e "$p" && ! -L "$p" ]]; then
        return 0
    fi
    run "${SUDO[@]}" rm -rf -- "$p"
}

confirm() {
    $ASSUME_YES && return 0
    $DRY_RUN    && return 0
    local reply
    printf '\n%sProceed with removal? [y/N]%s ' "$C_BLD" "$C_RST"
    read -r reply </dev/tty || reply=""
    [[ "$reply" =~ ^[Yy]([Ee][Ss])?$ ]]
}

# --------------------------------------------------------------------------
# Inventory
# --------------------------------------------------------------------------
declare -a FOUND_DIRS=()      # tarball install roots
declare -a FOUND_LINKS=()     # /usr/local/bin symlinks into the prefix
declare -a FOUND_RPMS=()      # rpm package names
declare -a FOUND_FILES=()     # profile / repo files
declare -a FOUND_ALTS=()      # "link|path" pairs registered in alternatives
declare -a FOUND_USERDATA=()  # caches, only with --purge-user-data

collect_dirs() {
    local d
    for d in "${OPT_PREFIX}/java" "${OPT_PREFIX}/gradle" "${OPT_PREFIX}/maven" "${OPT_PREFIX}/node"; do
        [[ -d "$d" ]] && FOUND_DIRS+=("$d")
    done
    return 0
}

collect_links() {
    local b link target
    for b in gradle mvn node npm npx java javac; do
        link="/usr/local/bin/${b}"
        [[ -L "$link" ]] || continue
        target="$(readlink -f "$link" 2>/dev/null || true)"
        if [[ -z "$target" ]]; then
            # dangling symlink - safe to drop
            FOUND_LINKS+=("$link")
        elif [[ "$target" == "${OPT_PREFIX}/"* ]]; then
            FOUND_LINKS+=("$link")
        fi
    done
    return 0
}

collect_rpms() {
    $KEEP_RPMS && return 0
    local -a all=()
    mapfile -t all < <(rpm -qa --qf '%{NAME}\n' 2>/dev/null | sort -u)

    local p
    for p in "${all[@]}"; do
        case "$p" in
            java-*-openjdk|java-*-openjdk-*|jdk-*|jdk)
                if $ALL_JDKS; then
                    FOUND_RPMS+=("$p")
                elif [[ "$p" == *"-${JAVA_MAJOR}-"* || "$p" == "jdk-${JAVA_MAJOR}" || "$p" == "java-${JAVA_MAJOR}-"* ]]; then
                    FOUND_RPMS+=("$p")
                fi
                ;;
            nodejs|nodejs-*|npm|npm-*)
                $KEEP_NODE || FOUND_RPMS+=("$p")
                ;;
            maven|maven-*|gradle|gradle-*)
                FOUND_RPMS+=("$p")
                ;;
        esac
    done
    return 0
}

collect_files() {
    if [[ -f "$PROFILE_D" ]]; then FOUND_FILES+=("$PROFILE_D"); fi
    if ! $KEEP_NODE; then
        local f
        while IFS= read -r f; do
            [[ -n "$f" ]] && FOUND_FILES+=("$f")
        done < <(find /etc/yum.repos.d -maxdepth 1 -name 'nodesource*.repo' 2>/dev/null || true)
    fi
    return 0
}

collect_alternatives() {
    have alternatives || return 0
    local name link path
    for name in java javac jar jshell keytool; do
        alternatives --display "$name" >/dev/null 2>&1 || continue
        link="/usr/bin/${name}"
        while IFS= read -r path; do
            [[ -n "$path" ]] || continue
            [[ "$path" == "${OPT_PREFIX}/"* ]] && FOUND_ALTS+=("${name}|${path}")
        done < <(alternatives --display "$name" 2>/dev/null \
                 | sed -nE 's#^(/[^ ]+) - (priority|family).*#\1#p')
    done
    return 0
}

collect_userdata() {
    $PURGE_USER_DATA || return 0
    local d
    for d in "${TARGET_HOME}/.gradle" "${TARGET_HOME}/.m2" "${TARGET_HOME}/.npm" \
             "${TARGET_HOME}/.npm-global" "${TARGET_HOME}/.sdkman" \
             "/usr/lib/node_modules" "/usr/local/lib/node_modules"; do
        [[ -d "$d" ]] && FOUND_USERDATA+=("$d")
    done
    return 0
}

print_list() {
    local label="$1"; shift
    local -a items=()
    local item
    for item in "$@"; do
        [[ -n "$item" ]] && items+=("$item")
    done
    if [[ ${#items[@]} -eq 0 ]]; then
        printf '  %-22s %s(none)%s\n' "$label" "$C_GRN" "$C_RST"
        return
    fi
    printf '  %-22s\n' "$label"
    for item in "${items[@]}"; do
        printf '      %s%s%s\n' "$C_YLW" "$item" "$C_RST"
    done
}

inventory() {
    step "scanning for components (prefix: ${OPT_PREFIX}, JDK ${JAVA_MAJOR})"

    collect_dirs
    collect_links
    collect_rpms
    collect_files
    collect_alternatives
    collect_userdata

    print_list "directories"     "${FOUND_DIRS[@]:-}"
    print_list "symlinks"        "${FOUND_LINKS[@]:-}"
    print_list "rpm packages"    "${FOUND_RPMS[@]:-}"
    print_list "files"           "${FOUND_FILES[@]:-}"
    print_list "alternatives"    "${FOUND_ALTS[@]:-}"
    $PURGE_USER_DATA && print_list "caches / user data" "${FOUND_USERDATA[@]:-}"

    TOTAL_ITEMS=$(( ${#FOUND_DIRS[@]} + ${#FOUND_LINKS[@]} + ${#FOUND_RPMS[@]} \
                  + ${#FOUND_FILES[@]} + ${#FOUND_ALTS[@]} + ${#FOUND_USERDATA[@]} ))
    printf '\n  %s%d item(s) selected for removal%s\n' "$C_BLD" "$TOTAL_ITEMS" "$C_RST"

    if [[ ${#FOUND_RPMS[@]} -gt 0 ]] && ! $DRY_RUN; then
        printf '\n  %sdnf transaction preview:%s\n' "$C_BLD" "$C_RST"
        "${SUDO[@]}" "$PKG" remove --assumeno "${FOUND_RPMS[@]}" 2>&1 \
            | sed -n '/Removing/,/Transaction Summary/p' | sed 's/^/    /' || true
    fi
}

# --------------------------------------------------------------------------
# Removal steps
# --------------------------------------------------------------------------
remove_alternatives() {
    [[ ${#FOUND_ALTS[@]} -gt 0 ]] || return 0
    step "deregistering alternatives"
    local entry name path
    for entry in "${FOUND_ALTS[@]}"; do
        name="${entry%%|*}"
        path="${entry#*|}"
        info "alternatives --remove ${name} ${path}"
        run "${SUDO[@]}" alternatives --remove "$name" "$path" || warn "could not remove ${name} -> ${path}"
    done
    # re-point whatever is left, if anything
    local n
    for n in java javac jar jshell keytool; do
        if alternatives --display "$n" >/dev/null 2>&1; then
            run "${SUDO[@]}" alternatives --auto "$n" || true
        fi
    done
}

remove_rpms() {
    [[ ${#FOUND_RPMS[@]} -gt 0 ]] || return 0
    step "removing rpm packages"
    info "${FOUND_RPMS[*]}"
    run "${SUDO[@]}" "$PKG" -y remove "${FOUND_RPMS[@]}"
}

reset_node_module() {
    $KEEP_NODE && return 0
    have dnf || return 0
    if dnf module list --enabled nodejs 2>/dev/null | grep -q nodejs; then
        step "resetting the nodejs dnf module stream"
        run "${SUDO[@]}" dnf -y module reset nodejs || warn "module reset failed"
    fi
}

remove_paths() {
    step "removing files and directories"
    local p
    for p in "${FOUND_LINKS[@]:-}" "${FOUND_FILES[@]:-}" "${FOUND_DIRS[@]:-}" "${FOUND_USERDATA[@]:-}"; do
        [[ -n "$p" ]] || continue
        info "rm -rf ${p}"
        safe_rm "$p" || true
    done
}

clean_dangling_links() {
    step "clearing dangling java/node symlinks"
    local d b link
    for d in /usr/bin /usr/local/bin; do
        for b in java javac jar jarsigner javadoc javap jshell jcmd jstack jps \
                 jlink jpackage keytool jdeps gradle mvn node npm npx; do
            link="${d}/${b}"
            if [[ -L "$link" && ! -e "$link" ]]; then
                info "dangling: ${link}"
                if [[ "$d" == "/usr/local/bin" ]]; then
                    safe_rm "$link" || true
                else
                    # /usr/bin entries are managed by alternatives
                    run "${SUDO[@]}" rm -f -- "$link" || true
                fi
            fi
        done
    done
}

verify() {
    step "verification"
    $DRY_RUN && { warn "dry-run: nothing was removed"; return 0; }

    hash -r
    local t leftovers=0
    for t in java javac jshell gradle mvn node npm; do
        if have "$t"; then
            printf '  %-8s %sstill present:%s %s\n' "$t" "$C_YLW" "$C_RST" "$(command -v "$t")"
            leftovers=$((leftovers + 1))
        else
            printf '  %-8s %sgone%s\n' "$t" "$C_GRN" "$C_RST"
        fi
    done

    [[ -f "$PROFILE_D" ]] && warn "${PROFILE_D} still exists"

    if [[ $leftovers -gt 0 ]]; then
        cat <<EOF

  Anything still listed above came from somewhere this script does not manage -
  a distro JDK for another release, an rpm outside the targeted version, SDKMAN,
  or a per-user install. Check with:

      rpm -qf "\$(readlink -f "\$(command -v java)")"
      alternatives --display java

EOF
    fi

    cat <<EOF
  Your current shell may still have JAVA_HOME / GRADLE_HOME / PATH entries from
  the old profile file. Start a new login shell, or unset them by hand:

      unset JAVA_HOME GRADLE_HOME MAVEN_HOME M2_HOME

EOF
}

# --------------------------------------------------------------------------
main() {
    $DRY_RUN && warn "dry-run mode: no changes will be made"

    TOTAL_ITEMS=0
    inventory

    if [[ "$TOTAL_ITEMS" -eq 0 ]]; then
        ok "nothing to remove"
        exit 0
    fi

    if ! confirm; then
        info "aborted, nothing was changed"
        exit 0
    fi

    remove_alternatives
    remove_rpms
    reset_node_module
    remove_paths
    clean_dangling_links
    verify
}

main "$@"
