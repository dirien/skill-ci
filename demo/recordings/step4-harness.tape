# Step 4: the harness changed. Bump Claude Code; every beta and stable version runs again on it.
#   vhs demo/recordings/step4-harness.tape
Source demo/recordings/settings.tape
Output demo/recordings/clips/step4-harness.mp4

Hide
Type "export PS1='\[\e[38;5;141m\]skill-ci\[\e[0m\] $ ' && clear"
Enter
Show

Type "make demo-model-swap MODE=bump TO=2.1.300"
Sleep 400ms
Enter
Sleep 10s

Hide
# The marker is printed as RE""ADY so the typed command itself never matches the Wait.
Type 'sleep 30 && demo/wait-runs.sh k8s-incident-triage v1.2.0 2.1.300 && demo/wait-runs.sh k8s-incident-triage v1.1.0 2.1.300 && demo/wait-runs.sh k8s-resource-naming v1.0.0 2.1.300 && sleep 20 && clear && echo RE""ADY'
Enter
Wait+Screen@60m /READY/
Type "clear"
Enter
Show

Type "make results SKILL=k8s-incident-triage"
Sleep 400ms
Enter
Sleep 10s

Hide
Type "make demo-model-swap MODE=restore >/dev/null && clear"
Enter
Show
