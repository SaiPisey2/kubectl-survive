# kubectl survive-zone - README demo.
#
# Four beats: what kubectl tells you, what is actually true, what to do about
# it, and proof that it worked. Every frame is real output from a real
# kube-apiserver and kube-scheduler running under KWOK. Nothing is mocked and
# no output is edited.

Require kubectl
Require kubectl-survive_zone

Output survive.gif

Set Shell "bash"
Set FontSize 19
Set Width 1500
Set Height 940
Set Padding 20
Set WindowBar Colorful
Set CursorBlink false
Set TypingSpeed 60ms
Set Theme "Catppuccin Frappe"

# Cluster build is plumbing, not workflow. Keep it off screen.
Hide
Type "export PATH=$PWD/bin:$PATH && export PS1='$ ' && clear" Enter
Sleep 2s
Show

# --- Beat 1: what kubectl tells you -------------------------------------
Type "kubectl get deployments"
Sleep 400ms
Enter
Sleep 2500ms

# --- Beat 2: what is actually true --------------------------------------
Type "kubectl survive-zone"
Sleep 600ms
Enter
Sleep 6s

# --- Beat 3: what to do about it ----------------------------------------
Hide
Type "clear" Enter
Sleep 500ms
Show
Type "kubectl survive-zone fix checkout-api"
Sleep 600ms
Enter
Sleep 7s

# --- Beat 4: proof ------------------------------------------------------
Hide
Type "clear" Enter
Sleep 500ms
Show
Type "kubectl patch deploy checkout-api --patch-file spread.yaml"
Sleep 400ms
Enter
Sleep 2s
Type "kubectl survive-zone"
Sleep 600ms
Enter
Sleep 5s
