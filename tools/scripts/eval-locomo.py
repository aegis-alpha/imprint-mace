#!/usr/bin/env python3
"""
LoCoMo10 benchmark eval for Imprint + OpenClaw.

Adapts the openclaw-eval pipeline (github.com/ZaynJarvis/openclaw-eval)
to measure Imprint's memory quality on the LoCoMo10 dataset (1540 QA cases).

Three phases:
  ingest  -- send conversation sessions to OpenClaw (hooks ingest into Imprint)
  qa      -- send questions to OpenClaw, log responses + token usage
  judge   -- grade answers with LLM judge, report per-category breakdown

Usage:
  python eval-locomo.py --label imprint-parallel ingest
  python eval-locomo.py --label imprint-parallel qa
  python eval-locomo.py --label imprint-parallel judge

Prerequisites:
  pip install requests openai

Environment variables:
  OPENCLAW_TOKEN       -- OpenClaw gateway bearer token (required)
  OPENAI_API_KEY       -- OpenAI API key for judge (required for judge phase)
  OPENCLAW_URL         -- OpenClaw base URL (default: http://127.0.0.1:3100)
  IMPRINT_URL          -- Imprint base URL (default: http://127.0.0.1:8180)
"""

import argparse
import asyncio
import json
import os
import sys
import time
import urllib.request
from pathlib import Path

import requests


LOCOMO_URL = "https://raw.githubusercontent.com/snap-research/locomo/main/data/locomo10.json"
DATA_DIR = Path(__file__).parent.parent.parent / "data"
OUTPUT_DIR = Path(__file__).parent.parent.parent / "output" / "locomo"


def get_openclaw_url() -> str:
    return os.environ.get("OPENCLAW_URL", "http://127.0.0.1:3100")


def get_imprint_url() -> str:
    return os.environ.get("IMPRINT_URL", "http://127.0.0.1:8180")


def get_openclaw_token() -> str:
    token = os.environ.get("OPENCLAW_TOKEN", "")
    if not token:
        print("Error: OPENCLAW_TOKEN env var required", file=sys.stderr)
        sys.exit(1)
    return token


def ensure_locomo_data() -> Path:
    path = DATA_DIR / "locomo10.json"
    if path.exists():
        print(f"Using cached {path}", file=sys.stderr)
        return path
    DATA_DIR.mkdir(parents=True, exist_ok=True)
    print(f"Downloading locomo10.json ...", file=sys.stderr)
    urllib.request.urlretrieve(LOCOMO_URL, path)
    print(f"Saved to {path}", file=sys.stderr)
    return path


def load_locomo(path: Path, sample_index: int | None = None) -> list[dict]:
    with open(path, "r", encoding="utf-8") as f:
        data = json.load(f)
    if sample_index is not None:
        if sample_index < 0 or sample_index >= len(data):
            print(
                f"Error: sample index {sample_index} out of range (0-{len(data)-1})",
                file=sys.stderr,
            )
            sys.exit(1)
        return [data[sample_index]]
    return data


def format_message(msg: dict) -> str:
    speaker = msg.get("speaker", "unknown")
    text = msg.get("text", "")
    line = f"{speaker}: {text}"
    img_urls = msg.get("img_url", [])
    if isinstance(img_urls, str):
        img_urls = [img_urls]
    blip = msg.get("blip_caption", "")
    if img_urls:
        for url in img_urls:
            caption = f": {blip}" if blip else ""
            line += f"\n{url}{caption}"
    elif blip:
        line += f"\n({blip})"
    return line


def build_sessions(item: dict) -> list[dict]:
    conv = item["conversation"]
    speakers = f"{conv['speaker_a']} & {conv['speaker_b']}"
    session_keys = sorted(
        [k for k in conv if k.startswith("session_") and not k.endswith("_date_time")],
        key=lambda k: int(k.split("_")[1]),
    )
    sessions = []
    for sk in session_keys:
        dt_key = f"{sk}_date_time"
        date_time = conv.get(dt_key, "")
        parts = [f"[group chat conversation: {date_time}]"]
        for msg in conv[sk]:
            parts.append(format_message(msg))
        combined = "\n\n".join(parts)
        sessions.append({
            "message": combined,
            "meta": {
                "sample_id": item["sample_id"],
                "session_key": sk,
                "date_time": date_time,
                "speakers": speakers,
            },
        })
    return sessions


def get_qa_pairs(item: dict) -> list[dict]:
    return [q for q in item.get("qa", []) if str(q.get("category", "")) != "5"]


def send_to_openclaw(
    base_url: str, token: str, user: str, message: str, timeout: int = 300
) -> tuple[str, dict]:
    url = f"{base_url}/v1/responses"
    headers = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {token}",
    }
    payload = {
        "model": "openclaw",
        "input": message,
        "stream": False,
        "user": user,
    }
    resp = requests.post(url, json=payload, headers=headers, timeout=timeout)
    resp.raise_for_status()
    body = resp.json()
    usage = body.get("usage", {"input_tokens": 0, "output_tokens": 0, "total_tokens": 0})
    text = extract_response_text(body)
    return text, usage


def extract_response_text(body: dict) -> str:
    try:
        for item in body.get("output", []):
            if item.get("type") == "message":
                for content in item.get("content", []):
                    if content.get("type") == "output_text":
                        return content.get("text", "")
        for item in body.get("output", []):
            if "text" in item:
                return item["text"]
            for content in item.get("content", []):
                if "text" in content:
                    return content["text"]
    except (KeyError, TypeError, IndexError):
        pass
    return "[ERROR: could not extract text from response]"


def reset_imprint(imprint_url: str):
    resp = requests.post(
        f"{imprint_url}/admin/reset",
        headers={"X-Confirm-Reset": "yes"},
        timeout=30,
    )
    resp.raise_for_status()
    print("  Imprint reset OK", file=sys.stderr)


def check_imprint_status(imprint_url: str) -> dict:
    resp = requests.get(f"{imprint_url}/status", timeout=10)
    resp.raise_for_status()
    return resp.json()


# -- Ingest phase ----------------------------------------------------------


def run_ingest(args):
    locomo_path = ensure_locomo_data()
    samples = load_locomo(locomo_path, args.sample)
    base_url = get_openclaw_url()
    imprint_url = get_imprint_url()
    token = get_openclaw_token()

    out_dir = OUTPUT_DIR / args.label
    out_dir.mkdir(parents=True, exist_ok=True)

    all_results = []

    for si, item in enumerate(samples):
        sample_id = item["sample_id"]
        user_key = f"eval-locomo-{sample_id}"
        sessions = build_sessions(item)

        print(f"\n{'='*60}", file=sys.stderr)
        print(
            f"Sample {si}/{len(samples)-1}: {sample_id} ({len(sessions)} sessions)",
            file=sys.stderr,
        )
        print(f"{'='*60}", file=sys.stderr)

        print("  Resetting Imprint ...", file=sys.stderr)
        reset_imprint(imprint_url)
        time.sleep(1)

        sample_results = []
        for i, sess in enumerate(sessions):
            sk = sess["meta"]["session_key"]
            msg_len = len(sess["message"])
            print(
                f"  [{i+1}/{len(sessions)}] {sk} ({msg_len} chars) ...",
                file=sys.stderr,
                end=" ",
            )

            t0 = time.time()
            try:
                resp_text, usage = send_to_openclaw(
                    base_url, token, user_key, sess["message"]
                )
                elapsed = time.time() - t0
                print(
                    f"OK ({elapsed:.1f}s, {usage.get('input_tokens', 0)} in)",
                    file=sys.stderr,
                )
                sample_results.append({
                    "sample_id": sample_id,
                    "session_key": sk,
                    "response": resp_text[:200],
                    "usage": usage,
                    "elapsed_s": round(elapsed, 1),
                })
            except Exception as e:
                elapsed = time.time() - t0
                print(f"FAIL ({elapsed:.1f}s): {e}", file=sys.stderr)
                sample_results.append({
                    "sample_id": sample_id,
                    "session_key": sk,
                    "error": str(e),
                    "elapsed_s": round(elapsed, 1),
                })

        status = check_imprint_status(imprint_url)
        facts = status.get("stats", {}).get("facts", 0)
        entities = status.get("stats", {}).get("entities", 0)
        print(
            f"  Imprint after ingest: {facts} facts, {entities} entities",
            file=sys.stderr,
        )

        all_results.append({
            "sample_id": sample_id,
            "user_key": user_key,
            "sessions_count": len(sessions),
            "imprint_facts": facts,
            "imprint_entities": entities,
            "sessions": sample_results,
        })

        ingest_file = out_dir / f"ingest-{sample_id}.json"
        with open(ingest_file, "w", encoding="utf-8") as f:
            json.dump(all_results[-1], f, indent=2, ensure_ascii=False)
        print(f"  Saved {ingest_file}", file=sys.stderr)

    summary_file = out_dir / "ingest-summary.json"
    with open(summary_file, "w", encoding="utf-8") as f:
        json.dump(all_results, f, indent=2, ensure_ascii=False)
    print(f"\nIngest summary: {summary_file}", file=sys.stderr)


# -- QA phase --------------------------------------------------------------


def run_qa(args):
    locomo_path = ensure_locomo_data()
    samples = load_locomo(locomo_path, args.sample)
    base_url = get_openclaw_url()
    imprint_url = get_imprint_url()
    token = get_openclaw_token()

    out_dir = OUTPUT_DIR / args.label
    out_dir.mkdir(parents=True, exist_ok=True)

    total_usage = {"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
    all_answers = []

    for si, item in enumerate(samples):
        sample_id = item["sample_id"]
        qas = get_qa_pairs(item)
        if args.count:
            qas = qas[: args.count]

        ingest_file = out_dir / f"ingest-{sample_id}.json"
        if not ingest_file.exists():
            print(
                f"Error: {ingest_file} not found. Run ingest first.", file=sys.stderr
            )
            sys.exit(1)
        with open(ingest_file, "r") as f:
            ingest_data = json.load(f)
        user_key = ingest_data["user_key"]

        print(f"\n{'='*60}", file=sys.stderr)
        print(
            f"Sample {si}/{len(samples)-1}: {sample_id} "
            f"({len(qas)} questions, user={user_key})",
            file=sys.stderr,
        )
        print(f"{'='*60}", file=sys.stderr)

        sample_answers = []
        sample_usage = {"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}

        for qi, qa in enumerate(qas):
            question = qa["question"]
            expected = qa["answer"]
            category = str(qa.get("category", "?"))
            evidence = qa.get("evidence", [])

            print(
                f"  [{qi+1}/{len(qas)}] cat={category} ...",
                file=sys.stderr,
                end=" ",
            )

            t0 = time.time()
            try:
                resp_text, usage = send_to_openclaw(
                    base_url, token, user_key, question
                )
                elapsed = time.time() - t0
                for k in sample_usage:
                    sample_usage[k] += usage.get(k, 0)
                print(f"OK ({elapsed:.1f}s)", file=sys.stderr)
                sample_answers.append({
                    "sample_id": sample_id,
                    "qi": qi,
                    "question": question,
                    "expected": expected,
                    "response": resp_text,
                    "category": category,
                    "evidence": evidence,
                    "usage": usage,
                    "elapsed_s": round(elapsed, 1),
                })
            except Exception as e:
                elapsed = time.time() - t0
                print(f"FAIL ({elapsed:.1f}s): {e}", file=sys.stderr)
                sample_answers.append({
                    "sample_id": sample_id,
                    "qi": qi,
                    "question": question,
                    "expected": expected,
                    "response": f"[ERROR: {e}]",
                    "category": category,
                    "evidence": evidence,
                    "elapsed_s": round(elapsed, 1),
                })

        for k in total_usage:
            total_usage[k] += sample_usage[k]

        print(
            f"  Tokens: in={sample_usage['input_tokens']} "
            f"out={sample_usage['output_tokens']}",
            file=sys.stderr,
        )

        all_answers.extend(sample_answers)

        qa_file = out_dir / f"qa-{sample_id}.json"
        with open(qa_file, "w", encoding="utf-8") as f:
            json.dump(sample_answers, f, indent=2, ensure_ascii=False)

    answers_file = out_dir / "answers.json"
    with open(answers_file, "w", encoding="utf-8") as f:
        json.dump(
            {
                "label": args.label,
                "total_questions": len(all_answers),
                "usage": total_usage,
                "results": all_answers,
            },
            f,
            indent=2,
            ensure_ascii=False,
        )

    print(f"\nTotal: {len(all_answers)} questions", file=sys.stderr)
    print(
        f"Total tokens: in={total_usage['input_tokens']} "
        f"out={total_usage['output_tokens']}",
        file=sys.stderr,
    )
    print(f"Answers: {answers_file}", file=sys.stderr)


# -- Judge phase -----------------------------------------------------------

JUDGE_SYSTEM = (
    "You are an expert grader that determines if answers to questions "
    "match a gold standard answer."
)

JUDGE_PROMPT = """\
Your task is to label an answer to a question as 'CORRECT' or 'WRONG'. You will be given:
(1) a question (posed by one user to another user),
(2) a 'gold' (ground truth) answer,
(3) a generated answer
which you will score as CORRECT/WRONG.

The point of the question is to ask about something one user should know about the other user
based on their prior conversations.
The gold answer will usually be a concise and short answer that includes the referenced topic.
The generated answer might be much longer, but you should be generous with your grading -
as long as it touches on the same topic as the gold answer, it should be counted as CORRECT.

For time related questions, the gold answer will be a specific date, month, year, etc.
The generated answer might use relative time references. Be generous - as long as it refers
to the same date or time period, count as CORRECT.

Question: {question}
Gold answer: {gold_answer}
Generated answer: {response}

First, provide a short explanation, then finish with CORRECT or WRONG.
Do NOT include both CORRECT and WRONG in your response.

Respond with JSON only: {{"is_correct": "CORRECT" or "WRONG", "reasoning": "your explanation"}}"""


async def grade_one(
    client, model: str, question: str, gold: str, response: str
) -> tuple[bool, str]:
    prompt = JUDGE_PROMPT.format(
        question=question, gold_answer=gold, response=response
    )
    try:
        resp = await client.chat.completions.create(
            model=model,
            messages=[
                {"role": "system", "content": JUDGE_SYSTEM},
                {"role": "user", "content": prompt},
            ],
            temperature=0,
        )
        content = resp.choices[0].message.content
        result = json.loads(content)
        label = result.get("is_correct", result.get("label", "WRONG"))
        reasoning = result.get("reasoning", "")
        return label.strip().lower() == "correct", reasoning
    except Exception as e:
        return False, f"judge error: {e}"


async def run_judge_async(args):
    from openai import AsyncOpenAI

    out_dir = OUTPUT_DIR / args.label
    answers_file = out_dir / "answers.json"
    if not answers_file.exists():
        print(f"Error: {answers_file} not found. Run qa first.", file=sys.stderr)
        sys.exit(1)

    with open(answers_file, "r") as f:
        data = json.load(f)
    answers = data.get("results", [])
    print(f"Loaded {len(answers)} answers from {answers_file}", file=sys.stderr)

    model = args.model
    api_key = os.environ.get("OPENAI_API_KEY", "")
    if not api_key:
        print("Error: OPENAI_API_KEY env var required for judge", file=sys.stderr)
        sys.exit(1)

    client = AsyncOpenAI(api_key=api_key)

    sem = asyncio.Semaphore(args.parallel)

    async def bounded_grade(item):
        async with sem:
            is_correct, reasoning = await grade_one(
                client, model, item["question"], item["expected"], item["response"]
            )
            return {**item, "grade": is_correct, "reasoning": reasoning}

    print(
        f"Judging with {model} (parallelism={args.parallel}) ...", file=sys.stderr
    )
    tasks = [bounded_grade(a) for a in answers]
    graded = await asyncio.gather(*tasks)

    correct = sum(1 for g in graded if g["grade"])
    total = len(graded)
    score = correct / total if total else 0.0

    categories: dict[str, dict[str, int]] = {}
    for g in graded:
        cat = g.get("category", "?")
        categories.setdefault(cat, {"correct": 0, "total": 0})
        categories[cat]["total"] += 1
        if g["grade"]:
            categories[cat]["correct"] += 1

    print(f"\n{'='*60}", file=sys.stderr)
    print(f"Results: {correct}/{total} correct ({score:.2%})", file=sys.stderr)
    print(f"{'='*60}", file=sys.stderr)

    if len(categories) > 1:
        print("\nPer-category:", file=sys.stderr)
        for cat in sorted(categories):
            c = categories[cat]
            pct = c["correct"] / c["total"] if c["total"] else 0.0
            print(
                f"  Category {cat}: {c['correct']}/{c['total']} ({pct:.2%})",
                file=sys.stderr,
            )

    usage = data.get("usage", {})
    summary = {
        "label": args.label,
        "judge_model": model,
        "score": round(score, 4),
        "correct": correct,
        "total": total,
        "per_category": {
            cat: {
                "correct": c["correct"],
                "total": c["total"],
                "score": round(c["correct"] / c["total"], 4) if c["total"] else 0,
            }
            for cat, c in sorted(categories.items())
        },
        "input_tokens": usage.get("input_tokens", 0),
        "output_tokens": usage.get("output_tokens", 0),
    }

    grades_file = out_dir / "grades.json"
    with open(grades_file, "w", encoding="utf-8") as f:
        json.dump(
            {"summary": summary, "grades": list(graded)},
            f,
            indent=2,
            ensure_ascii=False,
        )
    print(f"\nGrades: {grades_file}", file=sys.stderr)

    summary_file = out_dir / "summary.json"
    with open(summary_file, "w", encoding="utf-8") as f:
        json.dump(summary, f, indent=2, ensure_ascii=False)
    print(f"Summary: {summary_file}", file=sys.stderr)

    print(f"\n{json.dumps(summary, indent=2)}")


def run_judge(args):
    asyncio.run(run_judge_async(args))


# -- CLI -------------------------------------------------------------------


def main():
    parser = argparse.ArgumentParser(
        description="LoCoMo10 benchmark eval for Imprint + OpenClaw",
    )
    parser.add_argument(
        "--label", required=True, help="Configuration label (e.g. imprint-parallel)"
    )
    sub = parser.add_subparsers(dest="command", required=True)

    p_ingest = sub.add_parser("ingest", help="Ingest conversations into OpenClaw")
    p_ingest.add_argument(
        "--sample", type=int, default=None, help="Run single sample (0-based index)"
    )

    p_qa = sub.add_parser("qa", help="Run QA questions against OpenClaw")
    p_qa.add_argument(
        "--sample", type=int, default=None, help="Run single sample (0-based index)"
    )
    p_qa.add_argument(
        "--count", type=int, default=None, help="Limit questions per sample"
    )

    p_judge = sub.add_parser("judge", help="Grade answers with LLM judge")
    p_judge.add_argument(
        "--model", default="gpt-4.1-mini", help="Judge model (default: gpt-4.1-mini)"
    )
    p_judge.add_argument(
        "--parallel",
        type=int,
        default=20,
        help="Concurrent judge requests (default: 20)",
    )

    args = parser.parse_args()

    if args.command == "ingest":
        run_ingest(args)
    elif args.command == "qa":
        run_qa(args)
    elif args.command == "judge":
        run_judge(args)


if __name__ == "__main__":
    main()
