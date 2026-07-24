#!/bin/sh
# Renders infra/keycloak/realm-h2fleet.json (template with ${VAR} placeholders)
# into the shared `keycloak_realm` volume for Keycloak's --import-realm.
# Runs in the h2-keycloak-realm-init one-shot container (alpine, sed only).
#
# Constraint: secret values must not contain `|`, `&` or newlines
# (sed replacement metacharacters). Documented in .env.example.
set -eu

TEMPLATE=/template/realm-h2fleet.json
OUT=/out/realm-h2fleet.json

cp "$TEMPLATE" "$OUT"

for VAR in \
  KEYCLOAK_SERVICES_CLIENT_SECRET \
  KEYCLOAK_ADMIN_USER_PASSWORD \
  KEYCLOAK_OPERATOR_PASSWORD \
  KEYCLOAK_DRIVER_PASSWORD \
  KEYCLOAK_CITIZEN_PASSWORD
do
  eval "VAL=\${$VAR:?environment variable $VAR is required}"
  case "$VAL" in
    *[\&\|]*)
      echo "substitute-realm: $VAR contains a sed metacharacter (& or |); refusing" >&2
      exit 1
      ;;
  esac
  sed -i "s|\${$VAR}|$VAL|g" "$OUT"
done

# Safety net: fail loudly if any placeholder survived substitution.
if grep -q '\${KEYCLOAK_' "$OUT"; then
  echo "substitute-realm: unsubstituted placeholders remain in $OUT" >&2
  exit 1
fi

echo "substitute-realm: rendered realm to $OUT"
