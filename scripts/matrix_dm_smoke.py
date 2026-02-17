#!/usr/bin/env python3
import argparse
import json
import random
import string
import sys
import time
from typing import Any, Dict, Optional, Tuple
from urllib import error, parse, request


def http_json(
    homeserver: str,
    method: str,
    path: str,
    body: Optional[Dict[str, Any]] = None,
    access_token: str = "",
) -> Tuple[int, Dict[str, Any]]:
    url = homeserver.rstrip("/") + path
    headers = {"Content-Type": "application/json"}
    if access_token:
        headers["Authorization"] = f"Bearer {access_token}"
    data = None if body is None else json.dumps(body).encode("utf-8")
    req = request.Request(url, data=data, headers=headers, method=method)
    try:
        with request.urlopen(req, timeout=20) as resp:
            raw = resp.read().decode("utf-8") or "{}"
            return resp.getcode(), json.loads(raw)
    except error.HTTPError as exc:
        payload = exc.read().decode("utf-8")
        try:
            return exc.code, json.loads(payload)
        except Exception:
            return exc.code, {"raw": payload}


def random_suffix(length: int = 8) -> str:
    alphabet = string.ascii_lowercase + string.digits
    return "".join(random.choice(alphabet) for _ in range(length))


def main() -> int:
    parser = argparse.ArgumentParser(description="matrix dm smoke test for gopher appservice bot")
    parser.add_argument("--homeserver", required=True, help="homeserver base url, e.g. http://127.0.0.1:6167")
    parser.add_argument("--registration-token", required=True, help="matrix registration token for smoke user")
    parser.add_argument("--bot-user-id", required=True, help="bot matrix user id, e.g. @gopher:example.com")
    parser.add_argument(
        "--appservice-token",
        default="",
        help="optional appservice token; when set, smoke test force-joins bot before message send",
    )
    parser.add_argument("--message", default="hello from gopher smoke test", help="message text to send")
    parser.add_argument("--poll-attempts", type=int, default=12, help="sync poll attempts")
    parser.add_argument("--poll-timeout-ms", type=int, default=3000, help="timeout per sync poll in ms")
    args = parser.parse_args()

    suffix = random_suffix()
    username = f"gopher-smoke-{suffix}"
    password = f"Sm0kePass!{suffix}"

    code, register = http_json(
        args.homeserver,
        "POST",
        "/_matrix/client/v3/register",
        body={
            "username": username,
            "password": password,
            "auth": {"type": "m.login.registration_token", "token": args.registration_token},
            "inhibit_login": False,
        },
    )
    if code >= 300:
        print(f"register_failed status={code} payload={register}", file=sys.stderr)
        return 1

    access_token = register.get("access_token", "").strip()
    smoke_user_id = register.get("user_id", "").strip()
    if not access_token:
        print(f"register_missing_access_token payload={register}", file=sys.stderr)
        return 1

    code, created = http_json(
        args.homeserver,
        "POST",
        "/_matrix/client/v3/createRoom",
        body={
            "preset": "private_chat",
            "is_direct": True,
            "invite": [args.bot_user_id],
            "name": "gopher smoke test room",
        },
        access_token=access_token,
    )
    if code >= 300:
        print(f"create_room_failed status={code} payload={created}", file=sys.stderr)
        return 1

    room_id = created.get("room_id", "").strip()
    if not room_id:
        print(f"create_room_missing_room_id payload={created}", file=sys.stderr)
        return 1

    if args.appservice_token.strip():
        join_path = (
            "/_matrix/client/v3/rooms/"
            + parse.quote(room_id, safe="")
            + "/join?user_id="
            + parse.quote(args.bot_user_id, safe="")
        )
        code, joined = http_json(
            args.homeserver,
            "POST",
            join_path,
            body={},
            access_token=args.appservice_token.strip(),
        )
        if code >= 300:
            print(f"force_join_failed status={code} payload={joined}", file=sys.stderr)
            return 1

    for text in [args.message, args.message + " follow-up"]:
        txn_id = str(int(time.time() * 1000)) + random_suffix(4)
        send_path = (
            "/_matrix/client/v3/rooms/"
            + parse.quote(room_id, safe="")
            + "/send/m.room.message/"
            + parse.quote(txn_id, safe="")
        )
        code, sent = http_json(
            args.homeserver,
            "PUT",
            send_path,
            body={"msgtype": "m.text", "body": text},
            access_token=access_token,
        )
        if code >= 300:
            print(f"send_failed status={code} payload={sent}", file=sys.stderr)
            return 1

    members_path = "/_matrix/client/v3/rooms/" + parse.quote(room_id, safe="") + "/members"
    code, members = http_json(args.homeserver, "GET", members_path, access_token=access_token)
    if code >= 300:
        print(f"members_lookup_failed status={code} payload={members}", file=sys.stderr)
    bot_membership = "unknown"
    for event in members.get("chunk", []):
        if event.get("type") != "m.room.member":
            continue
        if event.get("state_key") != args.bot_user_id:
            continue
        bot_membership = str((event.get("content") or {}).get("membership", "unknown"))
        break

    next_batch = ""
    bot_replies = []
    for _ in range(args.poll_attempts):
        query = f"?timeout={args.poll_timeout_ms}"
        if next_batch:
            query += "&since=" + parse.quote(next_batch, safe="")
        code, synced = http_json(
            args.homeserver,
            "GET",
            "/_matrix/client/v3/sync" + query,
            access_token=access_token,
        )
        if code >= 300:
            print(f"sync_failed status={code} payload={synced}", file=sys.stderr)
            return 1
        next_batch = str(synced.get("next_batch", next_batch))
        joined = (((synced.get("rooms") or {}).get("join") or {}).get(room_id) or {})
        events = (((joined.get("timeline") or {}).get("events")) or [])
        for event in events:
            if event.get("type") != "m.room.message":
                continue
            sender = str(event.get("sender", "")).strip()
            body = str((event.get("content") or {}).get("body", "")).strip()
            if sender == args.bot_user_id and body:
                bot_replies.append(body)
        if bot_replies:
            break

    print(f"smoke_user_id={smoke_user_id}")
    print(f"smoke_room_id={room_id}")
    print(f"bot_membership={bot_membership}")
    print(f"bot_reply_count={len(bot_replies)}")
    if bot_replies:
        print(f"first_bot_reply={bot_replies[0]}")
        return 0

    print("bot_reply_missing", file=sys.stderr)
    return 2


if __name__ == "__main__":
    raise SystemExit(main())

