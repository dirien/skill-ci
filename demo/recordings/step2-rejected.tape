# Step 2: the gate says no before any tokens. Shows the stage commands on the prepared state
# (k8s-resource-naming v1.1.0 pushed and Rejected, see DEMO.md "Rebuild the state").
#   vhs demo/recordings/step2-rejected.tape
Source demo/recordings/settings.tape
Output demo/recordings/clips/step2-rejected.mp4

Hide
Type "export PS1='\[\e[38;5;141m\]skill-ci\[\e[0m\] $ ' && clear"
Enter
Show

Type "cat demo/variants/k8s-resource-naming/v1.1.0/scripts/bootstrap.sh"
Sleep 400ms
Enter
Sleep 5s
Type "make results SKILL=k8s-resource-naming"
Sleep 400ms
Enter
Sleep 9s
