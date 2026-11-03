"""Credential decryption shared with the control plane.

The control plane encrypts source credentials with AES-256-GCM
(ciphertext including the tag, nonce stored separately). This module
implements the decrypt side; the key lives in APP_ENCRYPTION_KEY and is
never persisted.
"""

from __future__ import annotations

import binascii
import json
from typing import Any

from cryptography.hazmat.primitives.ciphers.aead import AESGCM


class CredentialError(Exception):
    """Raised when credential decryption fails."""


def load_key(hex_key: str) -> bytes:
    """Decode the hex-encoded 256-bit encryption key."""
    try:
        key = bytes.fromhex(hex_key)
    except (ValueError, binascii.Error) as exc:
        raise CredentialError("APP_ENCRYPTION_KEY must be hex-encoded") from exc
    if len(key) != 32:
        raise CredentialError("APP_ENCRYPTION_KEY must decode to 32 bytes")
    return key


class CredentialCipher:
    """AES-256-GCM decryptor compatible with the Go control plane format."""

    def __init__(self, hex_key: str) -> None:
        self._aesgcm = AESGCM(load_key(hex_key))

    def decrypt(self, ciphertext: bytes, nonce: bytes) -> bytes:
        """Open a Go-produced box: ciphertext includes the 16-byte tag."""
        try:
            return self._aesgcm.decrypt(nonce, ciphertext, None)
        except Exception as exc:  # cryptography raises InvalidTag
            raise CredentialError("credential decryption failed (tampered or wrong key)") from exc

    def decrypt_json(self, ciphertext: bytes, nonce: bytes) -> dict[str, Any]:
        """Decrypt and parse a JSON credential payload."""
        raw = self.decrypt(ciphertext, nonce)
        try:
            parsed: dict[str, Any] = json.loads(raw)
        except json.JSONDecodeError as exc:
            raise CredentialError("credential payload is not valid JSON") from exc
        return parsed
