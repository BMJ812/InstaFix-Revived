import unittest
from unittest import mock

import app


class FakeHeaders(dict):
    def get(self, key, default=None):
        return super().get(str(key).lower(), default)


class FakeResponse:
    def __init__(self, status_code, headers):
        self.status_code = status_code
        self.headers = FakeHeaders({str(k).lower(): str(v) for k, v in headers.items()})


class VideoProxyHelpersTest(unittest.TestCase):
    def test_parse_content_range(self):
        self.assertEqual(app.parse_content_range("bytes 10-19/100"), (10, 19, 100))
        self.assertEqual(app.parse_content_range("bytes 10-19/*"), (10, 19, 0))
        self.assertIsNone(app.parse_content_range("bytes 19-10/100"))
        self.assertIsNone(app.parse_content_range("items 10-19/100"))

    def test_resume_range_header(self):
        self.assertEqual(app.resume_range_header(524288, 1048575), "bytes=524288-1048575")
        self.assertEqual(app.resume_range_header(524288, None), "bytes=524288-")

    def test_parse_request_range(self):
        self.assertEqual(app.parse_request_range("bytes=10-19"), (10, 19))
        self.assertEqual(app.parse_request_range("bytes=10-"), (10, None))
        self.assertIsNone(app.parse_request_range("bytes=19-10"))
        self.assertIsNone(app.parse_request_range("items=10-19"))

    def test_chunk_range_header_caps_to_chunk_and_requested_end(self):
        old_size = app.VIDEO_PROXY_UPSTREAM_CHUNK_BYTES
        try:
            app.VIDEO_PROXY_UPSTREAM_CHUNK_BYTES = 512
            self.assertEqual(app.chunk_range_header(0, None), "bytes=0-511")
            self.assertEqual(app.chunk_range_header(512, 1023), "bytes=512-1023")
            self.assertEqual(app.chunk_range_header(512, 700), "bytes=512-700")
        finally:
            app.VIDEO_PROXY_UPSTREAM_CHUNK_BYTES = old_size

    def test_response_transfer_plan_for_partial_content(self):
        response = FakeResponse(206, {"content-range": "bytes 524288-1048575/6796967"})

        self.assertEqual(app.response_transfer_plan(response), (524288, 524288, 1048575, 6796967))

    def test_response_transfer_plan_for_full_content(self):
        response = FakeResponse(200, {"content-length": "1818524"})

        self.assertEqual(app.response_transfer_plan(response), (1818524, 0, 1818523, 1818524))

    def test_video_candidates_are_sorted_by_smallest_known_resolution(self):
        versions = [
            {"url": "https://scontent.cdninstagram.com/large.mp4", "width": 1080, "height": 1920},
            {"url": "https://scontent.cdninstagram.com/unknown.mp4"},
            {"url": "https://scontent.cdninstagram.com/small.mp4", "width": 720, "height": 1280},
            {"url": "not a url", "width": 1, "height": 1},
        ]

        urls = [candidate[1] for candidate in app.video_candidates(versions)]

        self.assertEqual(
            urls,
            [
                "https://scontent.cdninstagram.com/small.mp4",
                "https://scontent.cdninstagram.com/large.mp4",
                "https://scontent.cdninstagram.com/unknown.mp4",
            ],
        )

    def test_unique_urls_preserves_order(self):
        self.assertEqual(app.unique_urls(["a", "b", "a", "", "c", "b"]), ["a", "b", "c"])

    def test_classify_redirect_location(self):
        self.assertEqual(app.classify_redirect_location("https://www.instagram.com/accounts/login/"), "login_required")
        self.assertEqual(app.classify_redirect_location("https://www.instagram.com/challenge/abc"), "challenge_required")
        self.assertEqual(app.classify_redirect_location("https://www.instagram.com/checkpoint/abc"), "checkpoint_required")
        self.assertEqual(app.classify_redirect_location("https://www.instagram.com/other"), "redirected")

    def test_classify_instagram_error_recognizes_geoblock(self):
        self.assertEqual(
            app.classify_instagram_error(400, b'{"message":"geoblock_required"}'),
            "geoblock_required",
        )

    def test_cache_helpers_expire_entries(self):
        cache = {}
        app.cache_set(cache, "a", {"ok": True}, 60)
        self.assertEqual(app.cache_get(cache, "a"), {"ok": True})
        app.cache_set(cache, "b", "expired", -1)
        self.assertIsNone(app.cache_get(cache, "b"))

    def test_cached_auth_payload_marks_cache_hit_without_mutating_cache(self):
        old_cache = dict(app.auth_success_cache)
        try:
            app.auth_success_cache.clear()
            app.cache_set(app.auth_success_cache, "POSTID", {"ok": True, "username": "cached"}, 60)
            payload = app.cached_auth_payload("POSTID")
            self.assertTrue(payload["cache_hit"])
            self.assertNotIn("cache_hit", app.auth_success_cache["POSTID"][1])
        finally:
            app.auth_success_cache.clear()
            app.auth_success_cache.update(old_cache)

    def test_oembed_uses_account_negative_cache_before_instagram_request(self):
        old_success = dict(app.auth_success_cache)
        old_negative = dict(app.auth_negative_cache)
        account = app.CookieAccount("account-a", "sessionid=a; ds_user_id=a", "/tmp/a.cookie")
        try:
            app.auth_success_cache.clear()
            app.auth_negative_cache.clear()
            app.cache_negative("POSTID", "private_media", "cached private post", account.account_id)
            with mock.patch("app.choose_cookie_account", return_value=account), mock.patch("app.requests.get") as request:
                with self.assertRaises(app.HelperError) as raised:
                    app.oembed("POSTID")
            self.assertEqual(raised.exception.code, "private_media")
            self.assertTrue(raised.exception.cached)
            request.assert_not_called()
        finally:
            app.auth_success_cache.clear()
            app.auth_success_cache.update(old_success)
            app.auth_negative_cache.clear()
            app.auth_negative_cache.update(old_negative)

    def test_auth_circuit_opens_for_login_required(self):
        old_until = app.auth_cooldown_until
        old_reason = app.auth_cooldown_reason
        try:
            app.auth_cooldown_until = 0
            app.auth_cooldown_reason = ""
            app.mark_auth_failure("login_required", "POSTID")
            remaining, reason = app.auth_circuit_status()
            self.assertGreater(remaining, 0)
            self.assertEqual(reason, "login_required")
        finally:
            app.auth_cooldown_until = old_until
            app.auth_cooldown_reason = old_reason

    def test_cookie_account_id_prefers_ds_user_id(self):
        self.assertEqual(app.cookie_account_id("csrftoken=a; ds_user_id=12345; sessionid=s"), "ig_12345")

    def test_choose_cookie_account_skips_cooling_account(self):
        a = app.CookieAccount("a", "sessionid=a; ds_user_id=a", "/tmp/a")
        b = app.CookieAccount("b", "sessionid=b; ds_user_id=b", "/tmp/b")
        old_cursor = app.cookie_cursor
        old_cooldowns = dict(app.account_cooldowns)
        try:
            app.cookie_cursor = 0
            app.account_cooldowns.clear()
            app.account_cooldowns["a"] = app.time.time() + 60
            with mock.patch("app.discover_cookie_accounts", return_value=[a, b]):
                self.assertEqual(app.choose_cookie_account().account_id, "b")
        finally:
            app.cookie_cursor = old_cursor
            app.account_cooldowns.clear()
            app.account_cooldowns.update(old_cooldowns)

    def test_cookie_pool_health_counts_cooldowns(self):
        a = app.CookieAccount("a", "sessionid=a; ds_user_id=a", "/tmp/a")
        b = app.CookieAccount("b", "sessionid=b; ds_user_id=b", "/tmp/b")
        old_cooldowns = dict(app.account_cooldowns)
        try:
            app.account_cooldowns.clear()
            app.account_cooldowns["a"] = app.time.time() + 60
            with mock.patch("app.discover_cookie_accounts", return_value=[a, b]):
                self.assertEqual(app.cookie_pool_health(), {"total": 2, "available": 1, "cooling_down": 1, "needs_login": 0})
        finally:
            app.account_cooldowns.clear()
            app.account_cooldowns.update(old_cooldowns)

    def test_oembed_skips_media_info_video_lookup_by_default(self):
        account = app.CookieAccount("a", "sessionid=a; ds_user_id=a", "/tmp/a")
        body = b'{"thumbnail_url":"https://scontent.cdninstagram.com/t.jpg","author_name":"user","title":"caption","media_id":"123"}'
        response = mock.Mock(status_code=200)
        response.iter_content.return_value = [body]
        old_fetch_video_info = app.FETCH_VIDEO_INFO
        try:
            app.FETCH_VIDEO_INFO = False
            with mock.patch("app.choose_cookie_account", return_value=account), mock.patch("app.auth_get", return_value=response), mock.patch("app.media_info_video_url") as media_info:
                payload = app.oembed("POSTID", bypass_cache=True)
            media_info.assert_not_called()
            self.assertEqual(payload["video_url"], "")
            self.assertEqual(payload["thumbnail_url"], "https://scontent.cdninstagram.com/t.jpg")
        finally:
            app.FETCH_VIDEO_INFO = old_fetch_video_info

    def test_media_info_video_lookup_does_not_cooldown_account(self):
        account = app.CookieAccount("a", "sessionid=a; ds_user_id=a", "/tmp/a")
        old_cooldowns = dict(app.account_cooldowns)
        try:
            app.account_cooldowns.clear()
            with mock.patch("app.requests.get") as get:
                get.return_value.status_code = 302
                get.return_value.headers = {"location": "https://www.instagram.com/accounts/login/"}
                get.return_value.close.return_value = None
                with self.assertRaises(app.HelperError):
                    app.media_info_video_url("123", account, "https://www.instagram.com/reel/POSTID/")
            self.assertNotIn("a", app.account_cooldowns)
        finally:
            app.account_cooldowns.clear()
            app.account_cooldowns.update(old_cooldowns)

    def test_oembed_does_not_use_media_info_fallback_by_default(self):
        account = app.CookieAccount("a", "sessionid=a; ds_user_id=a", "/tmp/a")
        body = b'{"message":"login_required"}'
        response = mock.Mock(status_code=401)
        response.iter_content.return_value = [body]
        old_fallback = app.FETCH_MEDIA_INFO_FALLBACK
        try:
            app.FETCH_MEDIA_INFO_FALLBACK = False
            with mock.patch("app.choose_cookie_account", return_value=account), mock.patch("app.auth_get", return_value=response), mock.patch("app.media_info_payload") as media_info:
                with self.assertRaises(app.HelperError):
                    app.oembed("POSTID", bypass_cache=True)
            media_info.assert_not_called()
        finally:
            app.FETCH_MEDIA_INFO_FALLBACK = old_fallback

    def test_oembed_uses_media_info_fallback_when_enabled(self):
        account = app.CookieAccount("a", "sessionid=a; ds_user_id=a", "/tmp/a")
        body = b'{"message":"private media"}'
        response = mock.Mock(status_code=403)
        response.iter_content.return_value = [body]
        media_payload = {
            "ok": True,
            "username": "public_user",
            "caption": "caption",
            "thumbnail_url": "https://scontent.cdninstagram.com/t.jpg",
            "video_url": "https://scontent.cdninstagram.com/v.mp4",
            "width": 720,
            "height": 1280,
        }
        old_fallback = app.FETCH_MEDIA_INFO_FALLBACK
        try:
            app.FETCH_MEDIA_INFO_FALLBACK = True
            with mock.patch("app.choose_cookie_account", return_value=account), mock.patch("app.auth_get", return_value=response), mock.patch("app.media_info_payload", return_value=media_payload) as media_info:
                payload = app.oembed("POSTID", bypass_cache=True)
            media_info.assert_called_once()
            self.assertEqual(payload["video_url"], "https://scontent.cdninstagram.com/v.mp4")
        finally:
            app.FETCH_MEDIA_INFO_FALLBACK = old_fallback

    def test_oembed_preserves_geoblock_when_media_info_fallback_fails(self):
        account = app.CookieAccount("a", "sessionid=a; ds_user_id=a", "/tmp/a")
        body = b'{"message":"geoblock_required"}'
        response = mock.Mock(status_code=400)
        response.iter_content.return_value = [body]
        old_fallback = app.FETCH_MEDIA_INFO_FALLBACK
        try:
            app.FETCH_MEDIA_INFO_FALLBACK = True
            with mock.patch("app.choose_cookie_account", return_value=account), mock.patch("app.auth_get", return_value=response), mock.patch(
                "app.media_info_payload",
                side_effect=app.HelperError("instagram_error", "media info HTTP 400"),
            ):
                with self.assertRaises(app.HelperError) as raised:
                    app.oembed("POSTID", bypass_cache=True)
            self.assertEqual(raised.exception.code, "geoblock_required")
            self.assertIn("media info fallback", str(raised.exception))
        finally:
            app.FETCH_MEDIA_INFO_FALLBACK = old_fallback

    def test_extract_username_from_nested_json(self):
        payload = {"config": {"viewer": {"id": "1", "username": "real.user_1"}}}

        self.assertEqual(app.extract_username_from_json(payload), "real.user_1")

    def test_cookie_homepage_fetches_username_when_html_unknown(self):
        account = app.CookieAccount("a", "sessionid=a; csrftoken=c; ds_user_id=a", "/tmp/a.cookie")
        homepage = mock.Mock(status_code=200, url="https://www.instagram.com/")
        homepage.iter_content.return_value = [b"<html>logged in app shell without username</html>"]
        current = mock.Mock(status_code=400, headers={}, url="https://www.instagram.com/api/v1/accounts/current_user/?edit=true")
        current.iter_content.return_value = [b'{"message":"useragent mismatch","status":"fail"}']
        shared = mock.Mock(status_code=200, headers={}, url="https://www.instagram.com/api/v1/web/data/shared_data/")
        shared.iter_content.return_value = [b'{"config":{"viewer":{"username":"real.user_1"}}}']
        old_rate = app.auth_rate_count
        old_window = app.auth_rate_window
        try:
            app.auth_rate_count = 0
            app.auth_rate_window = int(app.time.time() // 60)
            with mock.patch("app.find_cookie_account", return_value=account), mock.patch("app.requests.get", side_effect=[homepage, current, shared]), mock.patch("app.fetch_media_info", return_value={"id": "1"}):
                result = app.test_cookie_homepage("a")
            self.assertEqual(result["username"], "real.user_1")
        finally:
            app.auth_rate_count = old_rate
            app.auth_rate_window = old_window

    def test_cookie_homepage_rejects_anonymous_200_without_identity(self):
        account = app.CookieAccount("a", "sessionid=a; csrftoken=c; ds_user_id=a", "/tmp/a.cookie")
        homepage = mock.Mock(status_code=200, url="https://www.instagram.com/")
        homepage.iter_content.return_value = [b"<html>anonymous app shell</html>"]
        anonymous_api = []
        for _ in range(3):
            response = mock.Mock(status_code=200, headers={}, url="https://www.instagram.com/")
            response.iter_content.return_value = [b'{"status":"ok"}']
            anonymous_api.append(response)
        old_rate = app.auth_rate_count
        old_window = app.auth_rate_window
        old_quarantines = dict(app.account_quarantines)
        old_loaded = app.account_quarantines_loaded
        try:
            app.auth_rate_count = 0
            app.auth_rate_window = int(app.time.time() // 60)
            app.account_quarantines.clear()
            app.account_quarantines_loaded = True
            with mock.patch("app.find_cookie_account", return_value=account), mock.patch("app.requests.get", side_effect=[homepage] + anonymous_api):
                with self.assertRaisesRegex(app.HelperError, "identity could not be confirmed") as raised:
                    app.test_cookie_homepage("a")
            self.assertEqual(raised.exception.code, "session_invalid")
            self.assertTrue(app.account_needs_login("a"))
        finally:
            app.auth_rate_count = old_rate
            app.auth_rate_window = old_window
            app.account_quarantines.clear()
            app.account_quarantines.update(old_quarantines)
            app.account_quarantines_loaded = old_loaded

    def test_cookie_homepage_rejects_session_that_media_api_cannot_use(self):
        account = app.CookieAccount("a", "sessionid=a; csrftoken=c; ds_user_id=a", "/tmp/a.cookie")
        homepage = mock.Mock(status_code=200, url="https://www.instagram.com/")
        homepage.iter_content.return_value = [b'<html>"username":"real.user_1"</html>']
        old_rate = app.auth_rate_count
        old_window = app.auth_rate_window
        old_quarantines = dict(app.account_quarantines)
        old_loaded = app.account_quarantines_loaded
        try:
            app.auth_rate_count = 0
            app.auth_rate_window = int(app.time.time() // 60)
            app.account_quarantines.clear()
            app.account_quarantines_loaded = True
            with mock.patch("app.find_cookie_account", return_value=account), mock.patch("app.requests.get", return_value=homepage), mock.patch(
                "app.fetch_media_info", side_effect=app.HelperError("login_required", "media API rejected session")
            ):
                with self.assertRaisesRegex(app.HelperError, "media API probe failed") as raised:
                    app.test_cookie_homepage("a")
            self.assertEqual(raised.exception.code, "login_required")
            self.assertTrue(app.account_needs_login("a"))
        finally:
            app.auth_rate_count = old_rate
            app.auth_rate_window = old_window
            app.account_quarantines.clear()
            app.account_quarantines.update(old_quarantines)
            app.account_quarantines_loaded = old_loaded

    def test_cookie_homepage_accepts_restricted_probe_when_session_is_authenticated(self):
        account = app.CookieAccount("a", "sessionid=a; csrftoken=c; ds_user_id=a", "/tmp/a.cookie")
        homepage = mock.Mock(status_code=200, url="https://www.instagram.com/")
        homepage.iter_content.return_value = [b'<html>"username":"real.user_1"</html>']
        old_rate = app.auth_rate_count
        old_window = app.auth_rate_window
        old_quarantines = dict(app.account_quarantines)
        old_loaded = app.account_quarantines_loaded
        try:
            app.auth_rate_count = 0
            app.auth_rate_window = int(app.time.time() // 60)
            app.account_quarantines.clear()
            app.account_quarantines_loaded = True
            with mock.patch("app.find_cookie_account", return_value=account), mock.patch("app.requests.get", return_value=homepage), mock.patch(
                "app.fetch_media_info", side_effect=app.HelperError("geoblock_required", "probe content is restricted")
            ):
                result = app.test_cookie_homepage("a")
            self.assertEqual(result["code"], "ok")
            self.assertFalse(app.account_needs_login("a"))
        finally:
            app.auth_rate_count = old_rate
            app.auth_rate_window = old_window
            app.account_quarantines.clear()
            app.account_quarantines.update(old_quarantines)
            app.account_quarantines_loaded = old_loaded

    def test_update_cookie_validates_before_replacing_slot(self):
        import tempfile

        with tempfile.TemporaryDirectory() as directory:
            path = app.os.path.join(directory, "slot.cookie")
            with open(path, "w", encoding="utf-8") as f:
                f.write("sessionid=old; ds_user_id=old; csrftoken=old\n")
            with mock.patch("app.cookie_path_for_slot", return_value=(path, "ig_old")), mock.patch(
                "app.test_cookie_account", side_effect=app.HelperError("session_invalid", "candidate rejected")
            ):
                with self.assertRaisesRegex(app.HelperError, "candidate rejected"):
                    app.update_cookie_slot("slot", "sessionid=new; ds_user_id=new; csrftoken=new")
            with open(path, encoding="utf-8") as f:
                self.assertIn("sessionid=old", f.read())

    def test_update_cookie_rejects_wrong_account_before_replacing_slot(self):
        import tempfile

        with tempfile.TemporaryDirectory() as directory:
            path = app.os.path.join(directory, "ExpectedUser.cookie")
            with open(path, "w", encoding="utf-8") as f:
                f.write("sessionid=old; ds_user_id=old; csrftoken=old\n")
            validation = {"username": "DifferentUser", "status": 200, "code": "ok"}
            with mock.patch("app.cookie_path_for_slot", return_value=(path, "ig_old")), mock.patch(
                "app.test_cookie_account", return_value=validation
            ):
                with self.assertRaisesRegex(app.HelperError, "expects @expecteduser") as raised:
                    app.update_cookie_slot(
                        "ExpectedUser",
                        "sessionid=new; ds_user_id=new; csrftoken=new",
                        "ExpectedUser",
                    )
            self.assertEqual(raised.exception.code, "account_mismatch")
            with open(path, encoding="utf-8") as f:
                self.assertIn("sessionid=old", f.read())


if __name__ == "__main__":
    unittest.main()
