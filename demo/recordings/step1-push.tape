# Step 1: a push starts one SkillEval per agent and model. Run from the repo root, KUBECONFIG set.
# Pushes v1.1.1, a small real edit of v1.1.0; remove it afterwards so the stage can push v1.1.1 again
# (see DEMO.md, "Recording the fallback videos").
#   vhs demo/recordings/step1-push.tape
Source demo/recordings/settings.tape
Output demo/recordings/clips/step1-push.mp4

Hide
Type "export PS1='\[\e[38;5;141m\]skill-ci\[\e[0m\] $ ' && clear"
Enter
Show

Type "make demo-live VERSION=v1.1.1"
Sleep 400ms
Enter
Sleep 14s
Type "clear && make results SKILL=k8s-incident-triage"
Sleep 400ms
Enter
Sleep 8s
