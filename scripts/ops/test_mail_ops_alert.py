#!/usr/bin/env python3
"""No-network tests for external operator alert and live watchdog."""
import datetime as dt
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).parent


def load(name):
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"),
                                                  ROOT / (name + ".py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod

alert = load("send-mail-ops-alert")
watchdog = load("check-mail-ops-watchdog")
NOW = dt.datetime(2026, 11, 6, 12, 45, tzinfo=dt.timezone.utc)
ENV = {"OWNER_EMAIL": "owner@example.org", "FROM_EMAIL": "Atoman <alerts@atoman.org>",
       "RESEND_API_KEY": "TEST_ONLY_NONSENSITIVE"}


class Response:
    def __init__(self, status=200):
        self.status = status
    def __enter__(self): return self
    def __exit__(self, *args): return None


class AlertTests(unittest.TestCase):
    def test_failed_unit_whitelist(self):
        class Finished:
            returncode = 0
            stdout = ("● atoman-mail-ops-watchdog.service loaded failed failed\n"
                      "  atoman-backup.service loaded failed failed\n"
                      "evil.service loaded failed failed\n")
        self.assertEqual(alert.failed_services(lambda *a, **k: Finished()),
                         ["atoman-backup.service", "atoman-mail-ops-watchdog.service"])

    def test_resend_request_only_to_configured_owner(self):
        values = []
        def fake(request, timeout=0):
            values.append((request, timeout))
            return Response(201)
        alert.send_report("secret-test", ENV["FROM_EMAIL"], ENV["OWNER_EMAIL"],
                          "Failure", "No mailbox data", open_url=fake)
        request, timeout = values[0]
        self.assertEqual(request.full_url, "https://api.resend.com/emails")
        self.assertEqual(json.loads(request.data)["to"], ["owner@example.org"])
        self.assertNotIn("secret-test", request.data.decode())
        self.assertEqual(request.headers["Authorization"], "Bearer secret-test")
        self.assertEqual(timeout, 18)

    def test_dedup_only_after_success_and_different_failures_notify(self):
        calls = []
        failing = ["atoman-backup.service"]
        with tempfile.TemporaryDirectory() as tmp:
            state = Path(tmp) / "state.json"
            def send(*args):calls.append(args)
            def inspect():return failing
            result = alert.trigger(False, environ=ENV, now=NOW, state=state,
                                   inspect=inspect, send=send)
            self.assertEqual(result, "provider_accepted")
            self.assertEqual(alert.trigger(False,environ=ENV,
                                           now=NOW+dt.timedelta(minutes=5),
                                           state=state,inspect=inspect,send=send),"deduplicated")
            self.assertEqual(len(calls),1)
            self.assertEqual(state.stat().st_mode & 0o777,0o600)
            failing.append("atoman-mail-maintenance.service")
            alert.trigger(False,environ=ENV,now=NOW+dt.timedelta(minutes=6),
                          state=state,inspect=inspect,send=send)
            self.assertEqual(len(calls),2)
            self.assertEqual(alert.trigger(True,environ=ENV,now=NOW,state=state,
                                           send=send),"provider_accepted")
            self.assertEqual(len(calls),3)

    def test_does_not_mark_failure_delivered(self):
        with tempfile.TemporaryDirectory() as tmp:
            state = Path(tmp) / "state.json"
            def fail(*args):raise RuntimeError("provider error")
            with self.assertRaises(RuntimeError):
                alert.trigger(False,environ=ENV,now=NOW,state=state,
                              inspect=lambda:[],send=fail)
            self.assertFalse(state.exists())

    def test_local_smtp_transport_with_external_owner_recipient(self):
        env = {"ATOMAN_MAIL_ALERT_TRANSPORT": "local_smtp",
               "ATOMAN_MAIL_ALERT_RECIPIENT": "external-owner@gmail.com",
               "ATOMAN_MAIL_ALERT_FROM": "alerts@atoman.org"}
        with tempfile.TemporaryDirectory() as tmp:
            calls = []
            result = alert.trigger(True, environ=env, now=NOW,
                                   state=Path(tmp)/"state.json",
                                   send=lambda *args: calls.append(args))
            self.assertEqual(result, "local_queue_accepted")
            self.assertEqual(calls[0][2], "external-owner@gmail.com")
            self.assertEqual(calls[0][1], "alerts@atoman.org")

    def test_local_smtp_queues_without_exposing_message_to_other_recipient(self):
        observed = []
        class FakeSMTP:
            def __init__(self, host, port, timeout):
                observed.append((host, port, timeout))
            def __enter__(self):return self
            def __exit__(self,*args):return None
            def ehlo(self):return 250,"ok"
            def send_message(self, message, from_addr, to_addrs):
                observed.append((from_addr, to_addrs, message["To"], message["Subject"]))
                return {}
        alert.send_local_smtp_report("local_smtp", "alerts@atoman.org",
                                     "external-owner@gmail.com", "Test only",
                                     "No private contents", client=FakeSMTP)
        self.assertEqual(observed[0][:2], ("127.0.0.1", 25))
        self.assertEqual(observed[1][1], ["external-owner@gmail.com"])

    def test_no_secret_leak_in_bad_config(self):
        with self.assertRaisesRegex(RuntimeError, "configuration missing"):
            alert.trigger(True,environ={"OWNER_EMAIL":"bad@example",
                                         "FROM_EMAIL":"alerts@atoman.org",
                                         "RESEND_API_KEY":"should-never-print"},
                          now=NOW,send=lambda *a:None)


class WatchdogTests(unittest.TestCase):
    def fixture(self, file, *, stamp=NOW, status="ok", age=1):
        file.write_text(json.dumps({
            "checked_at": stamp.isoformat(),
            "status": status,
            "mail_capsule_publication_ready": True,
            "age_hours": age,
            "offsite_transfer_confirmed": False}))
    @staticmethod
    def fake_run(args, **kwargs):
        if args[0] == "docker":
            return subprocess.CompletedProcess(args, 0, stdout="true\n")
        return subprocess.CompletedProcess(args, 0)
    def test_ok_despite_known_separate_full_site_nas_backlog(self):
        with tempfile.TemporaryDirectory() as tmp:
            p = Path(tmp)/"health.json";self.fixture(p)
            self.assertEqual(watchdog.check(now=NOW,path=p,run=self.fake_run,
                                            open_url=lambda *a,**k:Response(200)),"ok")
    def test_stale_health_is_fault(self):
        with tempfile.TemporaryDirectory() as tmp:
            p = Path(tmp)/"health.json"
            self.fixture(p,stamp=NOW-dt.timedelta(hours=2))
            with self.assertRaisesRegex(RuntimeError, "stale"):
                watchdog.check(now=NOW,path=p,run=self.fake_run,
                               open_url=lambda *a,**k:Response(200))
    def test_inactive_service_is_fault(self):
        with tempfile.TemporaryDirectory() as tmp:
            p=Path(tmp)/"health.json";self.fixture(p)
            def fake_run(args, **kwargs):
                if args[0]=="systemctl":
                    return subprocess.CompletedProcess(args,1)
                return self.fake_run(args,**kwargs)
            with self.assertRaisesRegex(RuntimeError,"inactive"):
                watchdog.check(now=NOW,path=p,run=fake_run,
                               open_url=lambda *a,**k:Response(200))


if __name__=="__main__":
    unittest.main()
