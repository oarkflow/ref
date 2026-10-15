# Identity & Cryptographic Signing Service

A complete identity, membership, and offline licence signing service written in declarative BCL.

## Features
- **Built-in User Directory (`identity.users`)**: Email/password sign-in issuing JWTs with tenant roles.
- **Tenant Administration**: User invitations, role modification, account suspension/reactivation, and audit trail logging.
- **Ed25519 Cryptographic Signer (`crypto.signer`)**: Signs JWTs (EdDSA), offline-verifiable licences against published JWKS (`/.well-known/jwks.json`), and arbitrary payloads via `/api/sign`.

## Quick Start

### 1. Run with a Single Command (Makefile)

Inside this directory:
```bash
# Run directly with Aegis
make

# Or package and run in pure in-memory VFS (zero disk residue):
make secure
```

### 2. Manual Environment Configuration
```bash
# Generate Ed25519 signing key
openssl genpkey -algorithm ed25519 -out /tmp/identity.key

IDENTITY_SIGNING_KEY="$(cat /tmp/identity.key)" \
IDENTITY_ADMIN_EMAIL="admin@example.com" \
IDENTITY_ADMIN_PASSWORD="AdminPassword123!" \
make
```

### 3. Endpoints
- **JWKS Keyset**: `GET /.well-known/jwks.json`
- **Sign In**: `POST /api/auth/login`
- **Sign Arbitrary Payload**: `POST /api/sign`
