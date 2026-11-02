# Step 3: a better version takes stable, a worse one stays on beta. Shows the stage commands on the
# prepared state (k8s-incident-triage v1.1.0 stable, v1.2.0 Regressed, see DEMO.md "Rebuild the state").
#   vhs demo/recordings/step3-regression.tape
Source demo/recordings/settings.tape
Output demo/recordings/clips/step3-regression.mp4

Hide
Type "export PS1='\[\e[38;5;141m\]skill-ci\[\e[0m\] $ ' && clear"
Enter
Show

Type "make results SKILL=k8s-incident-triage"
Sleep 400ms
Enter
Sleep 9s
Type "demo/status.sh --tags-only"
Sleep 400ms
Enter
Sleep 9s
