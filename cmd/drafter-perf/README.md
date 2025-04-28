
ec2 c5a.xlarge
VM 1cpu, 1024MB ram.

```
Name                    WriteC    vm   Cow   SparseF    Set time   SetOver    Get time   GetOver     Runtime   memROps    memRMB   memWOps    memWMB     memChgBlk      memChgMB
--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
No Silo                                                   206.5s                202.9s                419.6s                                                                    
silo                             YES   YES       YES      224.2s        8%      209.3s        3%      446.7s       156      15.0    759771    4429.5             0           0.0
silo_no_vm_no_cow                                         209.5s        1%      209.4s        3%      429.4s      2423     267.9    424961    2425.1             0           0.0
silo_no_vmsf                           YES                209.2s        1%      203.3s        0%      422.9s        55       5.5    299522    2114.0           283         182.6
silo_no_vmsf_wc            YES         YES                207.0s        0%      203.6s        0%      421.4s       139      74.4       359     348.4           284         183.0
```