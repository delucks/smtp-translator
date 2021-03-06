#!/usr/bin/env python3

from smtplib import SMTP_SSL
from os import environ


sender = 'trump@whitehouse.gov'
receivers = [f'{environ["PUSHOVER_USER"]}@api.pushover.net']

message = """Subject: Test email

The quick brown fox jumps over the brown lazy dog.
"""

with SMTP_SSL('localhost', 1465) as smtp:
    recv_head = ', '.join(f'<{r}>' for r in receivers)
    smtp.sendmail(sender, receivers, f'From: <{sender}>\nTo: {recv_head}\n{message}')
