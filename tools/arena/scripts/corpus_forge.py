#!/usr/bin/env python3
"""Deterministically select and validate a small evidence-backed Arena corpus."""
import argparse, json
from pathlib import Path
import yaml

def load(path):
    data = yaml.safe_load(Path(path).read_text())
    if not isinstance(data, dict) or not isinstance(data.get("tasks"), list): raise ValueError("input must be an Arena corpus manifest with tasks")
    return data
def check(data):
    failures=[]; ids=[]
    for task in data["tasks"]:
        if not isinstance(task,dict): failures.append("task is not a mapping"); continue
        ident=str(task.get("id") or ""); ids.append(ident)
        if not ident or ids.count(ident)>1: failures.append(f"duplicate or empty task id: {ident!r}")
        if not all(task.get(k) for k in ("repo","baseline_sha","ticket","oracle")): failures.append(f"{ident}: missing required evidence")
        if task.get("verified_red") is not True or task.get("verified_green") is not True: failures.append(f"{ident}: missing RED/GREEN proof")
    return {"ok":not failures,"task_count":len(data["tasks"]),"task_ids":sorted(ids),"failures":failures}
def main():
 p=argparse.ArgumentParser(); s=p.add_subparsers(dest="cmd",required=True)
 a=s.add_parser("select"); a.add_argument("--input",required=True);a.add_argument("--out",required=True);a.add_argument("--limit",type=int,default=5);a.add_argument("--selection-id",required=True)
 b=s.add_parser("check"); b.add_argument("--input",required=True);b.add_argument("--json-out",default="")
 a=p.parse_args()
 try:
  if a.cmd=="select":
   source=load(a.input); chosen=sorted([x for x in source["tasks"] if isinstance(x,dict) and x.get("verified_red") is True and x.get("verified_green") is True and isinstance(x.get("oracle"),dict)],key=lambda x:x["id"])[:a.limit]
   if len(chosen)!=a.limit: raise ValueError(f"need {a.limit} eligible tasks, found {len(chosen)}")
   out={"kind":"arena_cost_corpus","version":1,"frozen":True,"selection_rule_committed_at":a.selection_id,"archetypes":source.get("archetypes",""),"source_manifest":a.input,"selection_notes":["Deterministic lexical selection from independently armed tasks.","Calibration only; promotion requires a separate heldout corpus."],"tasks":chosen}
   Path(a.out).parent.mkdir(parents=True,exist_ok=True);Path(a.out).write_text(yaml.safe_dump(out,sort_keys=False)); print(json.dumps({"ok":True,"out":a.out,"task_ids":[x["id"] for x in chosen]}))
  else:
   out=check(load(a.input));
   if a.json_out: Path(a.json_out).parent.mkdir(parents=True,exist_ok=True);Path(a.json_out).write_text(json.dumps(out,indent=2,sort_keys=True))
   print(json.dumps(out,sort_keys=True)); return 0 if out["ok"] else 1
 except Exception as e: print(json.dumps({"ok":False,"failures":[str(e)]}));return 1
 return 0
if __name__=="__main__": raise SystemExit(main())
