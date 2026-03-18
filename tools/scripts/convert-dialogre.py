#!/usr/bin/env python3
"""Convert DialogRE dataset to Imprint golden eval format.

Usage:
    python3 tools/scripts/convert-dialogre.py /path/to/dialogre/data/train.json testdata/golden/

Reads DialogRE train.json, selects dialogues with named entities and
non-trivial relations, converts to Imprint's paired .txt/.json format.

DialogRE is MIT licensed: https://github.com/nlpdata/dialogre
"""

import json
import os
import sys

RELATION_MAP = {
    "per:spouse": "related_to",
    "per:siblings": "related_to",
    "per:parents": "related_to",
    "per:children": "related_to",
    "per:other_family": "related_to",
    "per:friends": "related_to",
    "per:roommate": "related_to",
    "per:neighbor": "related_to",
    "per:girl/boyfriend": "related_to",
    "per:dates": "related_to",
    "per:acquaintance": "related_to",
    "per:boss": "manages",
    "per:subordinate": "works_on",
    "per:client": "uses",
    "per:employee_or_member_of": "part_of",
    "per:place_of_work": "located_at",
    "per:place_of_residence": "located_at",
    "per:visited_place": "located_at",
    "per:schools_attended": "part_of",
    "per:alumni": "related_to",
    "per:works": "works_on",
    "per:pet": "owns",
    "per:title": None,
    "per:age": None,
    "per:date_of_birth": None,
    "per:origin": None,
    "per:major": None,
    "per:alternate_names": None,
    "per:positive_impression": None,
    "per:negative_impression": None,
    "org:employees_or_members": "part_of",
    "org:students": "part_of",
    "gpe:residents_of_place": "located_at",
    "gpe:visitors_of_place": "located_at",
    "unanswerable": None,
}

ENTITY_TYPE_MAP = {
    "PER": "person",
    "ORG": "organization",
    "GPE": "location",
    "STRING": "concept",
    "VALUE": "concept",
}

RELATION_FACT_TYPE = {
    "per:spouse": "contact",
    "per:siblings": "contact",
    "per:parents": "contact",
    "per:children": "contact",
    "per:other_family": "contact",
    "per:friends": "contact",
    "per:roommate": "contact",
    "per:boss": "contact",
    "per:subordinate": "contact",
    "per:client": "contact",
    "per:employee_or_member_of": "contact",
    "per:place_of_work": "contact",
    "per:place_of_residence": "bio",
    "per:schools_attended": "bio",
    "per:alumni": "bio",
    "per:works": "contact",
    "per:title": "bio",
    "per:age": "bio",
    "per:date_of_birth": "bio",
    "per:origin": "bio",
    "per:pet": "bio",
    "per:girl/boyfriend": "contact",
    "per:dates": "contact",
    "per:acquaintance": "contact",
}

RELATION_DESCRIPTION = {
    "per:spouse": "{x} and {y} are married",
    "per:siblings": "{x} and {y} are siblings",
    "per:parents": "{x} is a parent of {y}",
    "per:children": "{x} has a child named {y}",
    "per:other_family": "{x} and {y} are family members",
    "per:friends": "{x} and {y} are friends",
    "per:roommate": "{x} and {y} are roommates",
    "per:neighbor": "{x} and {y} are neighbors",
    "per:girl/boyfriend": "{x} and {y} are in a relationship",
    "per:dates": "{x} and {y} are dating",
    "per:acquaintance": "{x} and {y} are acquaintances",
    "per:boss": "{x} is the boss of {y}",
    "per:subordinate": "{x} works under {y}",
    "per:client": "{x} is a client of {y}",
    "per:employee_or_member_of": "{x} is a member of {y}",
    "per:place_of_work": "{x} works at {y}",
    "per:place_of_residence": "{x} lives in {y}",
    "per:visited_place": "{x} visited {y}",
    "per:schools_attended": "{x} attended {y}",
    "per:alumni": "{x} and {y} are alumni of the same school",
    "per:works": "{x} works with {y}",
    "per:pet": "{x} has a pet named {y}",
    "per:title": "{x} holds the title of {y}",
    "per:age": "{x} is {y} years old",
    "per:origin": "{x} is originally from {y}",
}


def resolve_speaker_names(dialogue, rels):
    """Build Speaker N -> real name mapping from per:alternate_names."""
    name_map = {}
    for r in rels:
        if "per:alternate_names" in r["r"]:
            x, y = r["x"], r["y"]
            if x.startswith("Speaker") and not y.startswith("Speaker"):
                name_map[x] = y
            elif y.startswith("Speaker") and not x.startswith("Speaker"):
                name_map[y] = x
    return name_map


def resolve_name(name, name_map):
    return name_map.get(name, name)


def convert_dialogue(dialogue, rels, idx):
    """Convert one DialogRE dialogue to Imprint golden format."""
    name_map = resolve_speaker_names(dialogue, rels)

    text = "\n".join(dialogue)
    for speaker, real_name in name_map.items():
        text = text.replace(f"{speaker}:", f"{real_name}:")

    entities = {}
    facts = []
    relationships = []

    for r in rels:
        rel_types = r["r"]
        x_name = resolve_name(r["x"], name_map)
        y_name = resolve_name(r["y"], name_map)
        x_type = ENTITY_TYPE_MAP.get(r["x_type"], "concept")
        y_type = ENTITY_TYPE_MAP.get(r["y_type"], "concept")

        if x_name.startswith("Speaker"):
            continue
        if y_name.startswith("Speaker"):
            continue

        if x_name not in entities:
            aliases = []
            if r["x"] in name_map and name_map[r["x"]] != x_name:
                aliases.append(name_map[r["x"]])
            entities[x_name] = {
                "name": x_name,
                "entity_type": x_type,
                "aliases": aliases,
            }

        if y_name not in entities and not y_name.startswith("Speaker"):
            entities[y_name] = {
                "name": y_name,
                "entity_type": y_type,
                "aliases": [],
            }

        for rel_type in rel_types:
            if rel_type == "unanswerable":
                continue
            if rel_type == "per:alternate_names":
                if y_name not in entities[x_name]["aliases"] and y_name != x_name:
                    entities[x_name]["aliases"].append(y_name)
                continue

            imprint_rel = RELATION_MAP.get(rel_type)
            if imprint_rel and y_name in entities:
                rel_entry = {
                    "from_entity": x_name,
                    "to_entity": y_name,
                    "relation_type": imprint_rel,
                }
                if rel_entry not in relationships:
                    relationships.append(rel_entry)

            desc_template = RELATION_DESCRIPTION.get(rel_type)
            fact_type = RELATION_FACT_TYPE.get(rel_type, "contact")
            if desc_template:
                content = desc_template.format(x=x_name, y=y_name) + "."
                fact = {
                    "fact_type": fact_type,
                    "subject": x_name,
                    "content": content,
                    "confidence": 0.85,
                }
                if fact not in facts:
                    facts.append(fact)

    if len(entities) < 2 or (len(facts) == 0 and len(relationships) == 0):
        return None, None

    expected = {
        "facts": facts,
        "entities": list(entities.values()),
        "relationships": relationships,
        "_metadata": {
            "source": f"DialogRE train[{idx}] (MIT license)",
            "annotator": "converted from DialogRE annotations",
            "category": "dialogue",
        },
    }

    return text, expected


def main():
    if len(sys.argv) < 3:
        print(
            "Usage: python3 convert-dialogre.py <dialogre-train.json> <output-dir>",
            file=sys.stderr,
        )
        sys.exit(1)

    input_path = sys.argv[1]
    output_dir = sys.argv[2]
    max_examples = int(sys.argv[3]) if len(sys.argv) > 3 else 100

    with open(input_path) as f:
        data = json.load(f)

    os.makedirs(output_dir, exist_ok=True)

    existing = [
        f for f in os.listdir(output_dir) if f.endswith(".txt") or f.endswith(".json")
    ]
    start_num = 100

    converted = 0
    for i, d in enumerate(data):
        if converted >= max_examples:
            break

        dialogue, rels = d[0], d[1]
        if len(dialogue) > 12 or len(dialogue) < 3:
            continue

        text, expected = convert_dialogue(dialogue, rels, i)
        if text is None:
            continue

        num = start_num + converted
        stem = f"{num:03d}-dialogre-{i:04d}"

        txt_path = os.path.join(output_dir, f"{stem}.txt")
        json_path = os.path.join(output_dir, f"{stem}.json")

        with open(txt_path, "w") as f:
            f.write(text + "\n")

        with open(json_path, "w") as f:
            json.dump(expected, f, indent=2)
            f.write("\n")

        converted += 1

    print(f"Converted {converted} DialogRE dialogues to {output_dir}/")
    print(f"Files: {start_num:03d}-* through {start_num + converted - 1:03d}-*")


if __name__ == "__main__":
    main()
