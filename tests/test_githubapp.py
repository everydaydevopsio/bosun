import functools
import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import jwt
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import rsa

from bosun import githubapp


@functools.lru_cache(maxsize=1)
def _pem() -> str:
    """Generate a throwaway signing key rather than committing one."""
    key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    return key.private_bytes(
        serialization.Encoding.PEM,
        serialization.PrivateFormat.PKCS8,
        serialization.NoEncryption(),
    ).decode()


PEM = _pem()


class ConfiguredTests(unittest.TestCase):
    def test_not_configured_without_credentials(self):
        with mock.patch.dict(os.environ, {}, clear=True):
            self.assertFalse(githubapp.configured())

    def test_configured_with_id_and_key(self):
        with mock.patch.dict(os.environ, {"BOSUN_GITHUB_APP_ID": "1", "BOSUN_GITHUB_PRIVATE_KEY": PEM}):
            self.assertTrue(githubapp.configured())

    def test_key_can_come_from_a_file(self):
        with tempfile.NamedTemporaryFile("w", suffix=".pem", delete=False) as fh:
            fh.write(PEM)
            path = Path(fh.name)
        self.addCleanup(os.unlink, path)
        with mock.patch.dict(os.environ, {"BOSUN_GITHUB_APP_ID": "1",
                                          "BOSUN_GITHUB_PRIVATE_KEY_FILE": str(path)},
                             clear=True):
            self.assertTrue(githubapp.configured())

    def test_escaped_newlines_are_restored(self):
        """Kubernetes Secrets often carry the PEM with literal backslash-n."""
        with mock.patch.dict(os.environ, {"BOSUN_GITHUB_PRIVATE_KEY": PEM.replace("\n", "\\n")}):
            self.assertIn("-----BEGIN PRIVATE KEY-----\n", githubapp._private_key())


class JWTTests(unittest.TestCase):
    def test_signed_jwt_round_trips(self):
        token = githubapp.app_jwt(app_id="12345", private_key=PEM)
        claims = jwt.decode(token, options={"verify_signature": False})
        self.assertEqual(claims["iss"], "12345")
        self.assertLess(claims["iat"], claims["exp"])

    def test_expiry_is_within_githubs_ten_minute_limit(self):
        claims = jwt.decode(githubapp.app_jwt("1", PEM), options={"verify_signature": False})
        self.assertLessEqual(claims["exp"] - claims["iat"], 600)

    def test_missing_credentials_raise(self):
        with mock.patch.dict(os.environ, {}, clear=True):
            with self.assertRaises(githubapp.GitHubAppError):
                githubapp.app_jwt()

    def test_invalid_key_raises_a_clear_error(self):
        with self.assertRaises(githubapp.GitHubAppError):
            githubapp.app_jwt("1", "not-a-pem")


class TokenForTests(unittest.TestCase):
    def test_falls_back_to_pat_when_app_not_configured(self):
        with mock.patch.dict(os.environ, {"GITHUB_TOKEN": "ghp_x"}, clear=True):
            self.assertEqual(githubapp.token_for("acme/widget"), "ghp_x")

    def test_uses_installation_token_when_configured(self):
        env = {"BOSUN_GITHUB_APP_ID": "1", "BOSUN_GITHUB_PRIVATE_KEY": PEM}
        with mock.patch.dict(os.environ, env, clear=True):
            with mock.patch.object(githubapp, "installation_token", return_value="ghs_scoped") as mint:
                self.assertEqual(githubapp.token_for("acme/widget"), "ghs_scoped")
        mint.assert_called_once_with("acme/widget")

    def test_falls_back_when_minting_fails(self):
        env = {"BOSUN_GITHUB_APP_ID": "1", "BOSUN_GITHUB_PRIVATE_KEY": PEM, "GITHUB_TOKEN": "ghp_x"}
        with mock.patch.dict(os.environ, env, clear=True):
            with mock.patch.object(githubapp, "installation_token",
                                   side_effect=githubapp.GitHubAppError("not installed")):
                self.assertEqual(githubapp.token_for("acme/widget"), "ghp_x")

    def test_raises_when_minting_fails_and_there_is_no_fallback(self):
        env = {"BOSUN_GITHUB_APP_ID": "1", "BOSUN_GITHUB_PRIVATE_KEY": PEM}
        with mock.patch.dict(os.environ, env, clear=True):
            with mock.patch.object(githubapp, "installation_token",
                                   side_effect=githubapp.GitHubAppError("not installed")):
                with self.assertRaises(githubapp.GitHubAppError):
                    githubapp.token_for("acme/widget")


class InstallationTests(unittest.TestCase):
    def test_missing_installation_is_reported_clearly(self):
        response = mock.Mock(status_code=404)
        with mock.patch.object(githubapp.requests, "get", return_value=response):
            with self.assertRaises(githubapp.GitHubAppError) as ctx:
                githubapp.installation_id("acme/widget", "jwt")
        self.assertIn("not installed", str(ctx.exception))

    def test_installation_id_is_returned(self):
        response = mock.Mock(status_code=200)
        response.json.return_value = {"id": 4242}
        with mock.patch.object(githubapp.requests, "get", return_value=response):
            self.assertEqual(githubapp.installation_id("acme/widget", "jwt"), 4242)

    def test_token_request_is_scoped_to_the_repository(self):
        get = mock.Mock(status_code=200)
        get.json.return_value = {"id": 1}
        post = mock.Mock(status_code=201)
        post.json.return_value = {"token": "ghs_x", "expires_at": "2026-01-01T00:00:00Z"}
        env = {"BOSUN_GITHUB_APP_ID": "1", "BOSUN_GITHUB_PRIVATE_KEY": PEM}
        with mock.patch.dict(os.environ, env, clear=True):
            with mock.patch.object(githubapp.requests, "get", return_value=get), \
                 mock.patch.object(githubapp.requests, "post", return_value=post) as poster:
                self.assertEqual(githubapp.installation_token("acme/widget"), "ghs_x")
        body = poster.call_args.kwargs["json"]
        self.assertEqual(body["repositories"], ["widget"])
        self.assertEqual(body["permissions"]["contents"], "read")
        self.assertEqual(body["permissions"]["pull_requests"], "write")


if __name__ == "__main__":
    unittest.main()
